package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

func NewManager(cfg *Config) *Manager {
	ctx, cancel := context.WithCancel(context.Background())
	return &Manager{
		config: cfg,
		ctx:    ctx,
		cancel: cancel,
	}
}

// Start 启动管理器
func (m *Manager) Start() error {
	m.running = true

	// 启动工作进程
	if err := m.startWorkers(); err != nil {
		return err
	}

	// 启动健康检查
	m.wg.Add(1)
	go m.healthChecker()

	// 启动负载均衡器
	m.wg.Add(1)
	go m.loadBalancer()

	log.Println("PHP-CGI 管理器已启动")

	// 等待退出
	m.wg.Wait()
	return nil
}

// Init 初始化管理器
func (m *Manager) Init() error {
	log.Printf("启动 PHP-CGI 管理器: %s:%d", m.config.Host, m.config.Port)
	log.Printf("工作进程数: %d, 可用端口范围: %d-%d", m.config.WorkerCount, m.config.MinPort, m.config.MaxPort)
	log.Printf("PHP-CGI 路径: %s", m.config.PHPCGIPath)

	// 初始化工作进程列表
	m.workers = make([]*Worker, m.config.WorkerCount)

	return nil
}

// startWorkers 启动所有工作进程
func (m *Manager) startWorkers() error {
	if m.workers == nil {
		m.workers = make([]*Worker, m.config.WorkerCount)
	}

	usedPorts := make(map[int]bool)
	usedPorts[m.config.Port] = true // 避免使用主监听端口

	for i := 0; i < m.config.WorkerCount; i++ {
		// 获取可用端口
		port, err := getAvailablePort(m.config.Host, m.config.MinPort, m.config.MaxPort, usedPorts)
		if err != nil {
			log.Printf("获取工作进程 %d 的可用端口失败: %v", i+1, err)
			// 如果获取不到端口，尝试使用一个备用端口
			port = m.config.MinPort + i
			log.Printf("使用备用端口 %d 启动工作进程 %d", port, i+1)
		}

		usedPorts[port] = true
		worker := &Worker{
			ID:        i + 1,
			Port:      port,
			StartTime: time.Now(),
			Healthy:   false,
		}
		m.workers[i] = worker

		if err := m.startWorker(worker); err != nil {
			log.Printf("启动工作进程 %d 失败: %v", worker.ID, err)
			continue
		}
		time.Sleep(100 * time.Millisecond) // 避免同时启动太多进程
	}

	return nil
}

// startWorker 启动工作进程
func (m *Manager) startWorker(worker *Worker) error {
	// 尝试获取新的可用端口
	if worker.Cmd != nil && worker.RestartCount > 0 {
		usedPorts := make(map[int]bool)
		usedPorts[m.config.Port] = true // 避免使用主监听端口

		// 收集当前所有已使用的端口
		m.workerLock.RLock()
		for _, w := range m.workers {
			if w.ID != worker.ID && w.Port > 0 {
				usedPorts[w.Port] = true
			}
		}
		m.workerLock.RUnlock()

		// 获取新的可用端口
		newPort, err := getAvailablePort(m.config.Host, m.config.MinPort, m.config.MaxPort, usedPorts)
		if err == nil {
			worker.Port = newPort
			log.Printf("工作进程 %d 重启，使用新端口: %d", worker.ID, newPort)
		}
	}

	args := []string{
		"-b", fmt.Sprintf("127.0.0.1:%d", worker.Port),
	}

	if m.config.PHPIniPath != "" {
		args = append(args, "-c", m.config.PHPIniPath)
	}

	cmd := exec.CommandContext(m.ctx, m.config.PHPCGIPath, args...)

	// 设置进程属性
	if m.config.HideWindow {
		cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	}

	// 重定向输出到日志
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		return err
	}

	// 初始化工作进程状态
	worker.Cmd = cmd
	worker.StartTime = time.Now()
	worker.LastActiveTime = time.Now()
	worker.RestartCount++
	worker.RequestCount = 0 // 重置请求计数
	worker.CurrentRequest = nil

	log.Printf("启动工作进程 %d, 端口: %d, PID: %d",
		worker.ID, worker.Port, cmd.Process.Pid)

	// 监控进程退出
	m.wg.Add(1)
	go m.monitorWorker(worker)

	// 立即进行一次健康检查，确保工作进程准备就绪
	go func() {
		// 给进程一点时间启动
		time.Sleep(500 * time.Millisecond)
		m.checkSingleWorkerHealth(worker)
	}()

	return nil
}

// monitorWorker 监控工作进程
func (m *Manager) monitorWorker(worker *Worker) {
	defer m.wg.Done()

	err := worker.Cmd.Wait()

	m.workerLock.Lock()
	worker.Healthy = false
	m.workerLock.Unlock()

	if m.running {
		log.Printf("工作进程 %d 退出: %v, 5秒后重启", worker.ID, err)
		time.Sleep(5 * time.Second)
		m.restartWorker(worker)
	}
}

// restartWorker 重启工作进程
func (m *Manager) restartWorker(worker *Worker) {
	m.workerLock.Lock()
	defer m.workerLock.Unlock()

	// 检查请求数是否超过限制
	if atomic.LoadUint64(&worker.RequestCount) >= uint64(m.config.MaxRequests) {
		log.Printf("工作进程 %d 达到设定最大请求数 %d, 执行重启",
			worker.ID, m.config.MaxRequests)
	}

	if worker.Cmd != nil && worker.Cmd.Process != nil {
		worker.Cmd.Process.Kill()
	}

	if err := m.startWorker(worker); err != nil {
		log.Printf("重启工作进程 %d 失败: %v", worker.ID, err)
	}
}

// healthChecker 健康检查器
func (m *Manager) healthChecker() {
	defer m.wg.Done()

	ticker := time.NewTicker(time.Duration(m.config.HealthCheckInt) * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-m.ctx.Done():
			return
		case <-ticker.C:
			m.checkWorkersHealth()
		}
	}
}

// 检查所有工作进程的健康状态
func (m *Manager) checkWorkersHealth() {
	m.workerLock.RLock()
	workers := make([]*Worker, len(m.workers))
	copy(workers, m.workers)
	m.workerLock.RUnlock()

	for _, worker := range workers {
		m.checkWorkerHang(worker)
		m.checkSingleWorkerHealth(worker)
	}
}

// 检查单个工作进程的健康状态
func (m *Manager) checkSingleWorkerHealth(worker *Worker) {
	m.workerLock.Lock()
	defer m.workerLock.Unlock()

	if worker.Cmd == nil || worker.Cmd.Process == nil {
		worker.Healthy = false
		return
	}

	// Windows不支持 Signal(0) 检查进程存活, 直接通过端口连接来判断
	conn, err := net.DialTimeout("tcp",
		fmt.Sprintf("127.0.0.1:%d", worker.Port), 3*time.Second)

	if err != nil {
		wasHealthy := worker.Healthy
		worker.Healthy = false

		if wasHealthy {
			log.Printf("工作进程 %d 连接失败: %v", worker.ID, err)
		}
	} else {
		if !worker.Healthy {
			log.Printf("工作进程 %d 端口 %d 恢复正常", worker.ID, worker.Port)
		}
		worker.Healthy = true
		conn.Close()
	}
}

// 检查工作进程是否假死 例如请求处理超时
func (m *Manager) checkWorkerHang(worker *Worker) {
	// 检查进程是否有活跃的请求
	m.workerLock.RLock()
	request := worker.CurrentRequest
	healthy := worker.Healthy
	m.workerLock.RUnlock()

	// 如果没有活跃请求或者进程已经不健康, 则无需检查
	if request == nil || !healthy {
		return
	}

	// 计算请求处理时间
	elapsed := time.Since(request.StartTime)
	maxTime := time.Duration(m.config.MaxRequestTime) * time.Second

	// 如果请求处理时间超过配置的最大时间，认为进程假死
	if elapsed > maxTime {
		log.Printf("检测到工作进程 %d 假死: 请求 %s 处理时间 %v 超过最大限制 %v",
			worker.ID, request.RequestID, elapsed, maxTime)

		// 重启假死的工作进程
		m.restartWorker(worker)
	}
}

// loadBalancer 负载均衡器
func (m *Manager) loadBalancer() {
	defer m.wg.Done()

	listener, err := net.Listen("tcp", fmt.Sprintf("%s:%d", config.Host, config.Port))
	if err != nil {
		log.Fatalf("启动监听失败: %v", err)
	}
	defer listener.Close()

	log.Printf("负载均衡器运行在 %s:%d", config.Host, config.Port)

	for m.running {
		clientConn, err := listener.Accept()
		if err != nil {
			if m.running {
				log.Printf("接受连接错误: %v", err)
			}
			continue
		}

		go m.handleRequest(clientConn)
	}
}

func generateRequestID() string {
	timestamp := time.Now().UnixNano()
	random := rand.Intn(10000)
	return fmt.Sprintf("req-%d-%d", timestamp, random)
}

// handleRequest 处理客户端请求
func (m *Manager) handleRequest(clientConn net.Conn) {
	defer clientConn.Close()

	worker := m.selectWorker()
	if worker == nil {
		log.Println("没有可用的工作进程")
		clientConn.Write([]byte("HTTP/1.1 503 Service Unavailable\r\n\r\nNo backend available"))
		return
	}

	// 记录请求开始
	requestID := generateRequestID()
	requestInfo := &RequestInfo{
		StartTime:  time.Now(),
		RequestID:  requestID,
		InProgress: true,
	}

	// 更新工作进程状态
	m.workerLock.Lock()
	worker.CurrentRequest = requestInfo
	atomic.AddUint64(&worker.RequestCount, 1)
	m.workerLock.Unlock()

	// 记录开始时间
	startTime := time.Now()

	backendAddr := fmt.Sprintf("127.0.0.1:%d", worker.Port)
	backendConn, err := net.DialTimeout("tcp", backendAddr, 10*time.Second)
	if err != nil {
		// 请求失败，清除当前请求信息
		m.workerLock.Lock()
		worker.CurrentRequest = nil
		m.workerLock.Unlock()

		log.Printf("连接到工作进程 %d 失败: %v", worker.ID, err)
		clientConn.Write([]byte("HTTP/1.1 503 Service Unavailable\r\n\r\nBackend connection failed"))
		return
	}
	defer backendConn.Close()

	// 转发请求
	success := m.forwardData(clientConn, backendConn)

	// 请求处理完成，更新工作进程状态
	m.workerLock.Lock()
	worker.LastActiveTime = time.Now()
	worker.CurrentRequest = nil
	m.workerLock.Unlock()

	// 计算处理时间
	elapsed := time.Since(startTime)

	log.Printf("[worker %d] 完成处理 [%s] 请求, 状态: %v, 处理时间: %v",
		worker.ID, requestID, success, elapsed)
}

// selectWorker 选择一个工作进程
func (m *Manager) selectWorker() *Worker {
	m.workerLock.RLock()
	defer m.workerLock.RUnlock()

	// 首先尝试查找健康的工作进程
	for _, worker := range m.workers {
		if worker.Healthy {
			return worker
		}
	}

	// 如果没有健康的工作进程, 但有运行中的进程, 尝试进行一次快速检查
	for _, worker := range m.workers {
		if worker.Cmd != nil && worker.Cmd.Process != nil {
			// 立即在锁外进行一次快速检查
			go func(w *Worker) {
				m.checkSingleWorkerHealth(w)
			}(worker)
		}
	}

	// 尝试返回最近启动的工作进程, 即使它尚未被标记为健康
	var youngestWorker *Worker
	for _, worker := range m.workers {
		if worker.Cmd != nil && worker.Cmd.Process != nil {
			if youngestWorker == nil || worker.StartTime.After(youngestWorker.StartTime) {
				youngestWorker = worker
			}
		}
	}

	return youngestWorker
}

// forwardData 转发数据
func (m *Manager) forwardData(client, backend net.Conn) bool {
	var wg sync.WaitGroup
	wg.Add(2)
	var clientErr, backendErr error

	// 客户端 -> 后端
	go func() {
		defer wg.Done()
		buffer := make([]byte, 32*1024)
		_, clientErr = io.CopyBuffer(backend, client, buffer)
	}()

	// 后端 -> 客户端
	go func() {
		defer wg.Done()
		buffer := make([]byte, 32*1024)
		_, backendErr = io.CopyBuffer(client, backend, buffer)
	}()

	wg.Wait()

	if clientErr == nil && backendErr == nil {
		return true
	}

	// 记录错误信息
	if clientErr != nil {
		log.Printf("客户端写入错误: %v", clientErr)
	}
	if backendErr != nil {
		log.Printf("后端写入错误: %v", backendErr)
	}

	return false
}

// Stop 停止所有工作进程
func (m *Manager) Stop() {
	log.Println("正在停止 PHP-CGI 管理器...")

	m.running = false
	m.cancel()

	// 强制终止所有工作进程
	m.workerLock.Lock()
	for _, worker := range m.workers {
		if worker.Cmd != nil && worker.Cmd.Process != nil {
			log.Printf("终止工作进程 %d, PID: %d", worker.ID, worker.Cmd.Process.Pid)
			// 在Windows上直接使用Kill而不是Signal
			worker.Cmd.Process.Kill()
		}
	}
	m.workerLock.Unlock()

	log.Println("所有 PHP-CGI 进程已终止")
}

// handleSignals 处理信号
func handleSignals(manager *Manager) {
	signalChan := make(chan os.Signal, 1)
	signal.Notify(signalChan, syscall.SIGINT, syscall.SIGTERM)

	// 接收到信号直接停止所有进程
	sig := <-signalChan
	log.Printf("收到信号 %v, 正在停止...", sig)

	manager.Stop()

	time.Sleep(2 * time.Second)

	log.Println("程序将在 3 秒后退出")
	time.Sleep(3 * time.Second)
	os.Exit(0)
}

// 工具函数
func findPHPCGI() string {
	// 当前目录
	if path := findInPath("php-cgi.exe"); path != "" {
		return path
	}

	// PHP 常见安装路径
	commonPaths := []string{
		// 当前目录下的 php 目录
		filepath.Join(GetCurrPath(), "php", "php-cgi.exe"),
	}

	for _, path := range commonPaths {
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}

	return ""
}

func findPHPIni() string {
	if path := findInPath("php.ini"); path != "" {
		return path
	}

	// PHP 目录中查找
	phpDir := filepath.Dir(findPHPCGI())
	if phpDir != "" {
		iniPath := filepath.Join(phpDir, "php.ini")
		if _, err := os.Stat(iniPath); err == nil {
			return iniPath
		}
	}

	return ""
}

func findInPath(filename string) string {
	currDir, _ := os.Getwd()
	paths := []string{
		currDir,
		filepath.Join(currDir, "php"),
		filepath.Dir(os.Args[0]),
	}

	for _, path := range paths {
		fullPath := filepath.Join(path, filename)
		if _, err := os.Stat(fullPath); err == nil {
			return fullPath
		}
	}

	return ""
}
