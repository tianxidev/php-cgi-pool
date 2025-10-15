package main

import (
	"context"
	"os/exec"
	"sync"
	"time"
)

// Config 配置结构
type Config struct {
	Host           string // 监听主机
	Port           int    // 监听端口
	WorkerCount    int    // 工作进程数
	PHPCGIPath     string // PHP-CGI 可执行文件路径
	PHPIniPath     string // PHP 配置文件路径
	MinPort        int    // 最小可用端口
	MaxPort        int    // 最大可用端口
	HealthCheckInt int    // 健康检查间隔(秒)
	MaxRequests    int    // 每个进程最大请求数
	HideWindow     bool   // 是否隐藏窗口
	MaxRequestTime int    // 最大请求处理时间(秒), 超过此时间认为进程假死
}

// Worker 工作进程结构
type Worker struct {
	ID             int          // 工作进程ID
	Port           int          // 工作进程监听端口
	Cmd            *exec.Cmd    // 工作进程命令
	StartTime      time.Time    // 工作进程启动时间
	RequestCount   uint64       // 工作进程处理请求数
	Healthy        bool         // 工作进程是否健康
	RestartCount   int          // 工作进程重启次数
	LastActiveTime time.Time    // 最后活跃时间
	CurrentRequest *RequestInfo // 当前正在处理的请求
}

// RequestInfo 存储请求的相关信息
type RequestInfo struct {
	StartTime  time.Time // 请求开始时间
	RequestID  string    // 请求ID
	InProgress bool      // 请求是否正在处理
}

// Manager 工作进程管理器结构
type Manager struct {
	config     *Config            // 配置
	workers    []*Worker          // 工作进程列表
	workerLock sync.RWMutex       // 工作进程列表锁
	running    bool               // 管理器是否运行中
	ctx        context.Context    // 上下文
	cancel     context.CancelFunc // 取消函数
	wg         sync.WaitGroup     // 等待组
}
