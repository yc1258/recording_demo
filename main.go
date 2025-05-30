package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
	ffmpeg "github.com/u2takey/ffmpeg-go"
)

// DeviceType 定义设备类型
type DeviceType string

const (
	DeviceCamera      DeviceType = "camera"
	DeviceScreen      DeviceType = "screen"
	DeviceMicrophone  DeviceType = "microphone"
	DeviceSystemAudio DeviceType = "system_audio"
)

// Resolution 表示分辨率
type Resolution struct {
	Width  int `json:"width"`
	Height int `json:"height"`
}

// FrameRate 表示帧率
type FrameRate struct {
	Num int `json:"num"` // 分子
	Den int `json:"den"` // 分母
}

// VideoFormat 表示视频格式
type VideoFormat struct {
	Resolution  Resolution `json:"resolution"`
	FrameRate   FrameRate  `json:"frameRate"`
	PixelFormat string     `json:"pixelFormat"`
	Offset      *struct {  // 用于屏幕录制时的偏移量
		X int `json:"x"`
		Y int `json:"y"`
	} `json:"offset,omitempty"`
}

// Device 表示一个音视频设备
type Device struct {
	ID            string        `json:"id"`
	Name          string        `json:"name"`
	Type          DeviceType    `json:"type"`
	IsAvailable   bool          `json:"isAvailable"`
	VideoFormats  []VideoFormat `json:"videoFormats,omitempty"`
	CurrentFormat *VideoFormat  `json:"currentFormat,omitempty"`
	Format        interface{}   `json:"format,omitempty"` // 用于接收前端发送的格式数据
	cmd           *exec.Cmd
	Path          string `json:"path"`
}

// RecordingConfig 表示一路录制的配置
type RecordingConfig struct {
	VideoDevice *Device `json:"videoDevice"`
	AudioDevice *Device `json:"audioDevice"`
	OutputPath  string  `json:"outputPath"`
}

// RecordingSession 表示一个会话（可以是录制或预览）
type RecordingSession struct {
	ID          string            `json:"id"`
	Type        string            `json:"type"` // "recording" 或 "preview"
	Configs     []RecordingConfig `json:"configs"`
	Status      string            `json:"status"`
	StartTime   time.Time         `json:"startTime"`
	done        chan struct{}     // 用于停止的通道
	streamDone  chan struct{}     // 用于停止流传输的通道
	doneMutex   sync.Mutex        // 保护done通道的互斥锁
	streamMutex sync.Mutex        // 保护streamDone通道的互斥锁
	TempFiles   map[string]string // 存储临时文件路径
}

// VideoStreamState 用于跟踪视频流的状态
type VideoStreamState struct {
	LastKeyFrame  time.Time
	FrameCount    int64
	KeyFrameCount int64
	LastTimestamp int64
}

// VideoFramePacket 表示处理后的视频帧数据包
// VideoFrameFragment 表示视频帧分片
type VideoFrameFragment struct {
	Data     []byte `json:"data"`
	Index    int    `json:"index"`
	IsLast   bool   `json:"isLast"`
	TotalLen int    `json:"totalLen"`
}

// VideoFrame 表示完整的视频帧
type VideoFrame struct {
	Data       []byte `json:"data"`
	IsKeyFrame bool   `json:"isKeyFrame"`
}

// VideoFramePacket 表示处理后的视频帧数据包
type VideoFramePacket struct {
	Type      string              `json:"type"`
	DeviceID  string              `json:"deviceId"`
	Timestamp int64               `json:"timestamp"`
	Config    *DecoderConfig      `json:"config,omitempty"`
	Frame     *VideoFrame         `json:"frame,omitempty"`
	Fragment  *VideoFrameFragment `json:"fragment,omitempty"`
}

// DecoderConfig 表示解码器配置
type DecoderConfig struct {
	Codec  string `json:"codec"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
}

// StreamManager 管理视频流状态
type StreamManager struct {
	mu              sync.RWMutex
	streamStates    map[string]*VideoStreamState
	globalWSManager *WebSocketManager
}

const (
	VideoFrameBufferSize = 120
	AudioFrameBufferSize = 60
	MaxQueueSize         = 240
)

var (
	ffmpegPath string
	//ffprobePath string
	upgrader = websocket.Upgrader{
		CheckOrigin:  func(r *http.Request) bool { return true },
		Subprotocols: []string{"vp9"},
	}

	// 视频和音频缓冲区
	streamBuffer bytes.Buffer
	streamMutex  sync.Mutex
	pipeReader   *io.PipeReader
	pipeWriter   *io.PipeWriter

	// 全局管理器
	globalWSManager     *WebSocketManager
	globalStreamManager *StreamManager

	activeSessions map[string]*RecordingSession
	sessionMutex   sync.RWMutex
)

type ControlMessage struct {
	Action string `json:"action"`
	Codec  string `json:"codec,omitempty"`
}

// 添加 FFmpeg 命令跟踪
type FFmpegCmd struct {
	cmd   *exec.Cmd
	done  chan struct{}
	mutex sync.Mutex
}

var activeFFmpegCmds = struct {
	cmds  []*FFmpegCmd
	mutex sync.Mutex
}{
	cmds: make([]*FFmpegCmd, 0),
}

func addFFmpegCmd(cmd *exec.Cmd) *FFmpegCmd {
	ffCmd := &FFmpegCmd{
		cmd:  cmd,
		done: make(chan struct{}),
	}

	activeFFmpegCmds.mutex.Lock()
	activeFFmpegCmds.cmds = append(activeFFmpegCmds.cmds, ffCmd)
	activeFFmpegCmds.mutex.Unlock()

	return ffCmd
}

func removeFFmpegCmd(ffCmd *FFmpegCmd) {
	activeFFmpegCmds.mutex.Lock()
	defer activeFFmpegCmds.mutex.Unlock()

	for i, cmd := range activeFFmpegCmds.cmds {
		if cmd == ffCmd {
			activeFFmpegCmds.cmds = append(activeFFmpegCmds.cmds[:i], activeFFmpegCmds.cmds[i+1:]...)
			break
		}
	}
}

func stopAllFFmpegCmds() {
	activeFFmpegCmds.mutex.Lock()
	cmds := make([]*FFmpegCmd, len(activeFFmpegCmds.cmds))
	copy(cmds, activeFFmpegCmds.cmds)
	activeFFmpegCmds.mutex.Unlock()

	var wg sync.WaitGroup
	for _, ffCmd := range cmds {
		wg.Add(1)
		go func(ffCmd *FFmpegCmd) {
			defer wg.Done()
			ffCmd.mutex.Lock()
			defer ffCmd.mutex.Unlock()

			if ffCmd.cmd != nil && ffCmd.cmd.Process != nil {
				// 创建一个done通道用于等待进程退出
				done := make(chan struct{})
				go func() {
					ffCmd.cmd.Wait()
					close(done)
				}()

				// 首先检查进程是否已经退出
				select {
				case <-done:
					log.Printf("FFmpeg进程已经退出")
					return
				case <-time.After(100 * time.Millisecond):
					// 进程还在运行，继续处理
				}

				if runtime.GOOS == "windows" {
					pid := ffCmd.cmd.Process.Pid
					// 先尝试正常终止
					killCmd := exec.Command("taskkill", "/T", "/PID", strconv.Itoa(pid))
					if err := killCmd.Run(); err != nil {
						log.Printf("尝试正常终止进程失败: %v，将使用强制终止", err)
						// 如果正常终止失败，使用强制终止
						killCmd = exec.Command("taskkill", "/F", "/T", "/PID", strconv.Itoa(pid))
						if err := killCmd.Run(); err != nil {
							log.Printf("强制终止进程失败: %v", err)
						} else {
							log.Printf("已强制终止进程: %d", pid)
						}
					} else {
						log.Printf("已正常终止进程: %d", pid)
					}
				} else {
					// Linux/Unix系统使用信号
					if err := ffCmd.cmd.Process.Signal(syscall.SIGINT); err != nil {
						if err.Error() == "os: process already finished" {
							log.Printf("FFmpeg进程已经结束")
							return
						}
						log.Printf("发送SIGTERM到FFmpeg进程失败: %v", err)
					}
				}

				// 等待进程退出，设置超时
				select {
				case <-done:
					log.Printf("进程已退出")
				case <-time.After(2 * time.Second):
					log.Printf("等待进程退出超时")
					if runtime.GOOS != "windows" {
						// 在非Windows系统上尝试发送SIGKILL
						ffCmd.cmd.Process.Kill()
					}
				}
			}
		}(ffCmd)
	}

	// 等待所有FFmpeg进程停止，设置合理的超时时间
	doneWaiting := make(chan struct{})
	go func() {
		wg.Wait()
		close(doneWaiting)
	}()

	select {
	case <-doneWaiting:
		log.Printf("所有FFmpeg进程已正常停止")
	case <-time.After(10 * time.Second):
		log.Printf("等待FFmpeg进程停止超时")
	}

	// 清空命令列表
	activeFFmpegCmds.mutex.Lock()
	activeFFmpegCmds.cmds = make([]*FFmpegCmd, 0)
	activeFFmpegCmds.mutex.Unlock()
}

// 获取设备支持的视频格式
func getDeviceFormats(devicePath string) ([]VideoFormat, error) {
	// 检查是否是屏幕录制设备
	if strings.Contains(devicePath, ":") || devicePath == "desktop" || devicePath == "1" {
		// 获取屏幕分辨率
		var screenWidth, screenHeight int

		switch runtime.GOOS {
		case "linux":
			// 使用xrandr获取屏幕分辨率
			cmd := exec.Command("xrandr", "--current")
			output, err := cmd.CombinedOutput()
			if err == nil {
				// 解析xrandr输出
				re := regexp.MustCompile(`current (\d+) x (\d+)`)
				matches := re.FindStringSubmatch(string(output))
				if len(matches) == 3 {
					screenWidth, _ = strconv.Atoi(matches[1])
					screenHeight, _ = strconv.Atoi(matches[2])
				}
			}
		case "windows":
			// 在Windows上使用PowerShell获取屏幕分辨率
			cmd := exec.Command("powershell", "-Command", "(Get-WmiObject -Class Win32_VideoController).VideoModeDescription")
			output, err := cmd.CombinedOutput()
			if err == nil {
				re := regexp.MustCompile(`(\d+) x (\d+)`)
				matches := re.FindStringSubmatch(string(output))
				if len(matches) == 3 {
					screenWidth, _ = strconv.Atoi(matches[1])
					screenHeight, _ = strconv.Atoi(matches[2])
				}
			}
		case "darwin":
			// 在macOS上使用system_profiler获取屏幕分辨率
			cmd := exec.Command("system_profiler", "SPDisplaysDataType")
			output, err := cmd.CombinedOutput()
			if err == nil {
				re := regexp.MustCompile(`Resolution: (\d+) x (\d+)`)
				matches := re.FindStringSubmatch(string(output))
				if len(matches) == 3 {
					screenWidth, _ = strconv.Atoi(matches[1])
					screenHeight, _ = strconv.Atoi(matches[2])
				}
			}
		}

		// 如果无法获取屏幕分辨率，使用默认值
		if screenWidth == 0 || screenHeight == 0 {
			screenWidth = 1280
			screenHeight = 720
		}

		// 返回支持的屏幕录制格式
		return []VideoFormat{
			{
				Resolution: Resolution{
					Width:  screenWidth,
					Height: screenHeight,
				},
				FrameRate: FrameRate{
					Num: 30,
					Den: 1,
				},
				PixelFormat: "yuv420p",
			},
		}, nil
	}

	// 摄像头
	// 对于Windows设备，使用ffmpeg查询支持的格式
	if runtime.GOOS == "windows" {
		cmd := getFFmpegCmd(
			"-f", "dshow",
			"-list_options", "true",
			"-i", fmt.Sprintf("video=\"%s\"", devicePath),
		)
		output, err := cmd.CombinedOutput()
		if err != nil {
			// 如果获取失败，返回默认格式
			log.Printf("获取设备格式失败: %v，使用默认格式", err)
			return []VideoFormat{
				{
					Resolution:  Resolution{Width: 1280, Height: 720},
					FrameRate:   FrameRate{Num: 30, Den: 1},
					PixelFormat: "yuyv422",
				},
			}, nil
		}
		return parseVideoFormats(string(output))
	} else if runtime.GOOS == "linux" {
		// 对于摄像头设备，使用v4l2-ctl获取支持的格式
		cmd := exec.Command("v4l2-ctl", "-d", devicePath, "--list-formats-ext")
		output, err := cmd.CombinedOutput()
		if err != nil {
			// 如果获取失败，返回常用的预设格式
			return []VideoFormat{
				{
					PixelFormat: "mjpeg",
					Resolution: Resolution{
						Width:  1920,
						Height: 1080,
					},
					FrameRate: FrameRate{
						Num: 30,
						Den: 1,
					},
				},
				{
					PixelFormat: "yuyv422",
					Resolution: Resolution{
						Width:  1280,
						Height: 720,
					},
					FrameRate: FrameRate{
						Num: 30,
						Den: 1,
					},
				},
			}, nil
		}
		return parseVideoFormats(string(output))
	}
	return nil, fmt.Errorf("不支持的操作系统: %s", runtime.GOOS)
}

// 解析视频格式信息
func parseVideoFormats(output string) ([]VideoFormat, error) {
	var formats []VideoFormat
	lines := strings.Split(output, "\n")

	// 用于匹配分辨率的正则表达式
	resolutionRegex := regexp.MustCompile(`(\d+)x(\d+)`)
	// 用于匹配帧率的正则表达式
	framerateRegex := regexp.MustCompile(`(\d+(?:\.\d+)?)\s*fps`)

	for _, line := range lines {
		// 检查支持的格式（YUYV/MJPG等）
		switch runtime.GOOS {
		case "linux":
			if strings.Contains(line, "YUYV") || strings.Contains(line, "MJPG") {
				// 查找所有分辨率
				resMatches := resolutionRegex.FindAllStringSubmatch(line, -1)
				// 查找帧率
				fpsMatches := framerateRegex.FindAllStringSubmatch(line, -1)

				for _, resMatch := range resMatches {
					if len(resMatch) == 3 {
						width, _ := strconv.Atoi(resMatch[1])
						height, _ := strconv.Atoi(resMatch[2])

						// 对于每个分辨率，添加所有支持的帧率
						for _, fpsMatch := range fpsMatches {
							if len(fpsMatch) == 2 {
								fps, _ := strconv.ParseFloat(fpsMatch[1], 64)
								// 将浮点数帧率转换为分数形式
								num, den := convertFPSToFraction(fps)

								// 统一使用yuv420p作为像素格式
								format := VideoFormat{
									Resolution: Resolution{
										Width:  width,
										Height: height,
									},
									FrameRate: FrameRate{
										Num: num,
										Den: den,
									},
									PixelFormat: "yuv420p",
								}
								formats = append(formats, format)
							}
						}
					}
				}
			}
		case "windows":
			if strings.Contains(line, "fps") && strings.Contains(line, "x") {
				// 解析分辨率
				resMatches := resolutionRegex.FindStringSubmatch(line)
				if len(resMatches) != 3 {
					continue
				}
				width, _ := strconv.Atoi(resMatches[1])
				height, _ := strconv.Atoi(resMatches[2])
				// 解析帧率
				fpsMatches := framerateRegex.FindStringSubmatch(line)
				if len(fpsMatches) != 2 {
					continue
				}
				fps, _ := strconv.ParseFloat(fpsMatches[1], 64)
				num, den := convertFPSToFraction(fps)
				format := VideoFormat{
					Resolution: Resolution{
						Width:  width,
						Height: height,
					},
					FrameRate: FrameRate{
						Num: num,
						Den: den,
					},
					PixelFormat: "yuyv422", // 默认使用yuyv422格式
				}
				formats = append(formats, format)
			}
		}
	}

	// 如果没有找到任何格式，返回一些常用格式
	if len(formats) == 0 {
		formats = []VideoFormat{
			{
				Resolution:  Resolution{Width: 1920, Height: 1080},
				FrameRate:   FrameRate{Num: 30, Den: 1},
				PixelFormat: "yuv420p",
			},
			{
				Resolution:  Resolution{Width: 1280, Height: 720},
				FrameRate:   FrameRate{Num: 30, Den: 1},
				PixelFormat: "yuv420p",
			},
			{
				Resolution:  Resolution{Width: 640, Height: 480},
				FrameRate:   FrameRate{Num: 30, Den: 1},
				PixelFormat: "yuv420p",
			},
		}
	}

	return formats, nil
}

// 将浮点数帧率转换为分数形式
func convertFPSToFraction(fps float64) (num, den int) {
	// 将浮点数乘以1000并四舍五入，以处理小数点后三位
	value := int(math.Round(fps * 1000))
	den = 1000
	num = value

	// 计算最大公约数
	gcd := func(a, b int) int {
		for b != 0 {
			a, b = b, a%b
		}
		return a
	}

	// 化简分数
	d := gcd(num, den)
	return num / d, den / d
}

func main() {
	// 检查并清理端口
	if err := cleanupPort(9494); err != nil {
		log.Printf("清理端口失败: %v", err)
		return
	}

	// 创建一个用于接收终止信号的通道
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	// 创建一个用于优雅关闭的通道
	done := make(chan bool)

	// 配置WebSocket升级器
	upgrader.CheckOrigin = func(r *http.Request) bool {
		return true // 允许所有来源的WebSocket连接
	}

	// 启动HTTP服务器
	server := &http.Server{
		Addr: ":9494",
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// 添加CORS头
			w.Header().Set("Access-Control-Allow-Origin", "*")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type")

			// 处理OPTIONS请求
			if r.Method == "OPTIONS" {
				w.WriteHeader(http.StatusOK)
				return
			}

			// 处理WebSocket连接
			if r.URL.Path == "/ws" {
				handleWebSocket(w, r)
			} else {
				// 静态文件服务
				http.FileServer(http.Dir("frontend")).ServeHTTP(w, r)
			}
		}),
	}

	go func() {
		for {
			now := time.Now().Format("2006-01-02 15:04:05.000") // 含毫秒
			os.WriteFile("recording_time.txt", []byte(now), 0644)
			time.Sleep(10 * time.Millisecond) // 10ms更新一次（平衡精度和性能）
		}
	}()

	// 在goroutine中启动服务器
	go func() {
		fmt.Printf("服务启动: http://localhost:9494\n")
		if err := server.ListenAndServe(); err != http.ErrServerClosed {
			log.Printf("HTTP服务器错误: %v\n", err)
		}
		done <- true
	}()

	// 等待终止信号
	<-sigChan
	log.Println("收到终止信号，开始优雅关闭...")

	// 执行清理操作
	cleanup()

	// 关闭HTTP服务器
	if err := server.Shutdown(context.Background()); err != nil {
		log.Printf("HTTP服务器关闭错误: %v\n", err)
	}

	// 等待服务器完全关闭
	<-done
	log.Println("服务已安全关闭")
}

// WSMessage 定义WebSocket消息结构
type WSMessage struct {
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
	MessageID string          `json:"messageId,omitempty"`
}

// WSAckMessage 定义确认消息结构
type WSAckMessage struct {
	Type      string `json:"type"`
	MessageID string `json:"messageId"`
	Status    string `json:"status"`
	Error     string `json:"error,omitempty"`
}

// VideoFrameMessage 表示视频帧消息
type VideoFrameMessage struct {
	Type string `json:"type"`
	Data struct {
		DeviceID string `json:"deviceId"`
		Data     string `json:"data"`
		DataType string `json:"dataType"`
	} `json:"data"`
}

// 处理WebSocket消息（后备处理器）
func handleWebSocket(w http.ResponseWriter, r *http.Request) {
	// 升级HTTP连接为WebSocket
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("WebSocket升级失败: %v", err)
		return
	}

	// 使用 sync.WaitGroup 来等待所有goroutine完成
	var wg sync.WaitGroup
	// 创建一个用于通知关闭的通道
	shutdown := make(chan struct{})

	// 设置WebSocket连接
	globalWSManager.SetConnection(conn)

	// 设置连接关闭处理
	defer func() {
		close(shutdown) // 通知所有goroutine关闭
		wg.Wait()       // 等待所有goroutine完成

		// 设置写入超时并发送关闭消息
		writeTimeout := time.Now().Add(2 * time.Second)
		conn.SetWriteDeadline(writeTimeout)
		closeMsg := websocket.FormatCloseMessage(websocket.CloseGoingAway, "连接关闭")
		conn.WriteMessage(websocket.CloseMessage, closeMsg)

		// 等待消息发送完成
		time.Sleep(100 * time.Millisecond)
		conn.Close()
		globalWSManager.Close()
	}()

	// 设置更长的写入超时时间
	conn.SetWriteDeadline(time.Time{}) // 清除写入超时
	conn.SetReadDeadline(time.Time{})  // 清除读取超时

	// 在连接建立时发送初始设备列表
	devices, err := getAvailableDevices()
	if err != nil {
		log.Printf("获取设备列表失败: %v", err)
		conn.WriteJSON(map[string]interface{}{
			"type":    "error",
			"message": fmt.Sprintf("获取设备列表失败: %v", err),
		})
		return
	}

	// 序列化设备列表
	devicesJSON, err := json.Marshal(devices)
	if err != nil {
		log.Printf("序列化设备列表失败: %v", err)
		return
	}

	// 发送初始设备列表
	initialDeviceListMsg := WSMessage{
		Type:      "devices",
		MessageID: fmt.Sprintf("devices_%d", time.Now().UnixNano()),
		Payload:   json.RawMessage(fmt.Sprintf(`{"devices": %s}`, string(devicesJSON))),
	}

	if err := conn.WriteJSON(initialDeviceListMsg); err != nil {
		log.Printf("发送初始设备列表失败: %v", err)
		return
	}

	log.Printf("初始设备列表已发送")

	// 注册消息处理器已经在前面完成，这里不需要重复注册

	// 处理客户端消息
	for {
		var msg WSMessage
		if err := conn.ReadJSON(&msg); err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				log.Printf("WebSocket错误: %v", err)
			}
			return
		}

		log.Printf("收到WebSocket消息: %+v", msg)

		// 忽略确认消息
		if msg.Type == "ack" {
			continue
		}

		// 处理消息类型别名
		msgType := msg.Type
		switch msgType {
		case "RECORDING.START":
			msgType = "start_recording"
		case "RECORDING.STOP":
			msgType = "stop_recording"
		}

		// 处理其他消息
		if err := globalWSManager.handleMessage(msgType, msg.Payload); err != nil {
			log.Printf("处理消息失败: %v", err)

			// 发送错误响应
			errMsg := WSMessage{
				Type:      "error",
				MessageID: msg.MessageID,
				Payload:   json.RawMessage(fmt.Sprintf(`{"message": "%v"}`, err)),
			}

			if err := conn.WriteJSON(errMsg); err != nil {
				log.Printf("发送错误响应失败: %v", err)
			}
			continue
		}

		// 发送成功确认
		if msg.MessageID != "" {
			ack := WSAckMessage{
				Type:      "ack",
				MessageID: msg.MessageID,
				Status:    "success",
			}
			if err := conn.WriteJSON(ack); err != nil {
				log.Printf("发送确认消息失败: %v", err)
			}
		}
	}
}

// 清理函数
// 清理临时文件
func cleanupTempFiles() error {
	// 清理recordings目录中的所有临时文件
	patterns := []string{
		"recordings/temp_*",
		"recordings/*.raw",
		"recordings/*.ivf",
		"recordings/*.debug",
	}

	var lastError error
	maxRetries := 3
	retryDelay := 500 * time.Millisecond

	for _, pattern := range patterns {
		if files, err := filepath.Glob(pattern); err == nil {
			for _, file := range files {
				var err error
				for retry := 0; retry < maxRetries; retry++ {
					err = os.Remove(file)
					if err == nil {
						log.Printf("清理临时文件: %s", file)
						break
					}

					if runtime.GOOS == "windows" && strings.Contains(err.Error(), "process cannot access") {
						// Windows下文件被占用，等待一段时间后重试
						log.Printf("文件 %s 被占用，等待重试 (%d/%d)", file, retry+1, maxRetries)
						time.Sleep(retryDelay)
						continue
					}

					// 其他错误直接退出重试
					break
				}

				if err != nil {
					log.Printf("清理临时文件失败 %s: %v", file, err)
					lastError = err
				}
			}
		}
	}
	return lastError
}

func cleanup() {
	log.Println("开始执行清理操作...")

	// 停止所有活动会话
	sessionMutex.Lock()
	var wg sync.WaitGroup
	for id, session := range activeSessions {
		wg.Add(1)
		go func(id string, session *RecordingSession) {
			defer wg.Done()
			log.Printf("正在停止录制会话: %s", id)
			session.Stop()

			// 释放所有使用的设备
			for _, config := range session.Configs {
				if config.VideoDevice != nil && config.VideoDevice.Type == DeviceCamera {
					if err := releaseVideoDevice(config.VideoDevice.Path); err != nil {
						log.Printf("释放视频设备失败: %v", err)
					} else {
						log.Printf("成功释放视频设备: %s", config.VideoDevice.Path)
					}
					// 等待设备完全释放
					time.Sleep(500 * time.Millisecond)
				}
			}
		}(id, session)
	}

	// 等待所有会话停止，设置合理的超时时间
	doneWaiting := make(chan struct{})
	go func() {
		wg.Wait()
		close(doneWaiting)
	}()

	select {
	case <-doneWaiting:
		log.Printf("所有录制会话已正常停止")
	case <-time.After(15 * time.Second):
		log.Printf("等待录制会话停止超时")
	}

	// 清空会话映射
	activeSessions = make(map[string]*RecordingSession)
	sessionMutex.Unlock()

	// 停止所有FFmpeg进程
	stopAllFFmpegCmds()

	// 关闭WebSocket连接
	log.Println("正在关闭WebSocket连接...")
	globalWSManager.Close()

	// 清理缓冲区
	streamMutex.Lock()
	streamBuffer.Reset()
	streamMutex.Unlock()

	// 关闭所有管道
	if pipeReader != nil {
		log.Println("正在关闭管道读取器...")
		pipeReader.Close()
		pipeReader = nil
	}
	if pipeWriter != nil {
		log.Println("正在关闭管道写入器...")
		pipeWriter.Close()
		pipeWriter = nil
	}

	// 等待资源释放
	log.Println("等待资源释放...")
	time.Sleep(2 * time.Second)

	// 清理临时文件
	log.Println("清理临时文件...")
	if err := cleanupTempFiles(); err != nil {
		log.Printf("警告：清理临时文件时发生错误: %v", err)
	}

	// 强制进行垃圾回收
	runtime.GC()

	log.Println("清理操作完成")
}

// 修改 convertToMP4WithResolution 函数中的相关部分
type mediaConverter struct {
	inputPath  string
	outputPath string
	audioPath  string
	resolution *Resolution
}

func newMediaConverter(inputPath, outputPath, audioPath string, resolution *Resolution) (*mediaConverter, error) {
	mc := &mediaConverter{
		inputPath:  inputPath,
		outputPath: outputPath,
		audioPath:  audioPath,
		resolution: resolution,
	}

	if err := mc.validate(); err != nil {
		return nil, err
	}

	return mc, nil
}

func (mc *mediaConverter) validate() error {
	if mc.inputPath == "" {
		return fmt.Errorf("视频输入路径为空")
	}
	if mc.audioPath == "" {
		return fmt.Errorf("音频输入路径为空")
	}
	if mc.outputPath == "" {
		return fmt.Errorf("输出路径为空")
	}
	if _, err := os.Stat(mc.audioPath); err != nil {
		return fmt.Errorf("音频文件不存在或无法访问: %v", err)
	}

	return nil
}

func (mc *mediaConverter) validateOutput() error {
	if _, err := os.Stat(mc.outputPath); err != nil {
		return fmt.Errorf("输出文件不存在: %v", err)
	}

	// 验证文件大小
	info, err := os.Stat(mc.outputPath)
	if err != nil {
		return fmt.Errorf("无法获取输出文件信息: %v", err)
	}

	if info.Size() == 0 {
		return fmt.Errorf("输出文件大小为0")
	}

	// 使用ffmpeg验证文件完整性
	cmd := exec.Command(ffmpegPath, "-v", "error", "-i", mc.outputPath, "-c", "copy", "-f", "null", "-")
	if err := cmd.Run(); err != nil {
		// 如果验证失败，尝试修复文件
		log.Printf("文件验证失败，尝试修复: %v", err)
		repairCmd := exec.Command(ffmpegPath,
			"-i", mc.outputPath,
			"-c", "copy",
			"-movflags", "+faststart+delay_moov+default_base_moof",
			"-fflags", "+genpts+igndts+discardcorrupt+flush_packets",
			"-avoid_negative_ts", "make_zero",
			"-max_interleave_delta", "0",
			mc.outputPath+".fixed.mp4")

		if err := repairCmd.Run(); err != nil {
			return fmt.Errorf("文件修复失败: %v", err)
		}

		// 替换原文件
		if err := os.Rename(mc.outputPath+".fixed.mp4", mc.outputPath); err != nil {
			return fmt.Errorf("替换修复文件失败: %v", err)
		}

		// 再次验证
		cmd = exec.Command(ffmpegPath, "-v", "error", "-i", mc.outputPath, "-c", "copy", "-f", "null", "-")
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("修复后文件仍然无效: %v", err)
		}
	}

	log.Printf("文件验证成功: %s", mc.outputPath)
	return nil
}

func (mc *mediaConverter) cleanup() {
	for _, file := range []string{mc.inputPath, mc.audioPath} {
		if err := os.Remove(file); err != nil {
			log.Printf("清理临时文件失败 %s: %v", file, err)
		} else {
			log.Printf("已清理临时文件: %s", file)
		}
	}
}

func (mc *mediaConverter) convert() error {
	log.Printf("开始转换MP4 - 输入: %s, 输入音频: %s, 输出: %s, 分辨率: %dx%d",
		mc.inputPath, mc.audioPath, mc.outputPath, mc.resolution.Width, mc.resolution.Height)

	// 检查输入文件是否存在
	if _, err := os.Stat(mc.inputPath); err != nil {
		return fmt.Errorf("视频文件不存在或无法访问: %v", err)
	}
	if _, err := os.Stat(mc.audioPath); err != nil {
		return fmt.Errorf("音频文件不存在或无法访问: %v", err)
	}

	videoStream := ffmpeg.Input(mc.inputPath)
	var audioStream *ffmpeg.Stream
	switch runtime.GOOS {
	case "linux":
		audioStream = ffmpeg.Input(mc.audioPath)
	case "windows":
		audioStream = ffmpeg.Input(mc.audioPath, ffmpeg.KwArgs{
			"f":               "s16le",
			"ar":              "48000",
			"ac":              "2",
			"sample_fmt":      "s16",
			"channel_layout":  "stereo",
			"probesize":       "32M",
			"analyzeduration": "10M",
		})
	}

	var stream *ffmpeg.Stream
	switch runtime.GOOS {
	case "linux":
		stream = ffmpeg.Output([]*ffmpeg.Stream{videoStream, audioStream}, mc.outputPath, ffmpeg.KwArgs{
			"map":                         []string{"-0", "-1", "0:v:0", "1:a:0"}, // 映射视频和音频流
			"c:v":                         "copy",                                 // 直接复制视频流
			"c:a":                         "aac",                                  // 使用AAC编码音频
			"strict":                      "experimental",                         // 允许实验性编码器
			"fps_mode":                    "cfr",
			"max_muxing_queue_size":       4096,
			"fflags":                      "+genpts+igndts+discardcorrupt",
			"max_interleave_delta":        0,
			"copyts":                      "",
			"avoid_negative_ts":           "make_zero",
			"f":                           "mp4",
			"movflags":                    "+faststart+write_colr+use_metadata_tags",
			"use_wallclock_as_timestamps": 1,
			"map_metadata":                "0",
		})
	case "windows":
		stream = ffmpeg.Output([]*ffmpeg.Stream{videoStream, audioStream}, mc.outputPath, ffmpeg.KwArgs{
			"map":                         []string{"-0", "-1", "0:v:0", "1:a:0"},
			"c:v":                         "copy",
			"c:a":                         "aac",
			"b:a":                         "192k",
			"strict":                      "experimental",
			"shortest":                    "",
			"fps_mode":                    "cfr",
			"async":                       1000,
			"max_muxing_queue_size":       8192,
			"fflags":                      "+genpts+igndts+discardcorrupt+flush_packets",
			"max_interleave_delta":        0,
			"copyts":                      "",
			"avoid_negative_ts":           "make_zero",
			"f":                           "mp4",
			"movflags":                    "+faststart+use_metadata_tags+default_base_moof",
			"use_wallclock_as_timestamps": 1,
			"thread_queue_size":           8192,
			"probesize":                   "32M",
			"analyzeduration":             "10M",
			"start_at_zero":               "",
		})
	}
	stream = stream.OverWriteOutput().ErrorToStdOut().SetFfmpegPath(ffmpegPath)
	cmd := stream.Compile()
	err := cmd.Run()
	if err != nil {
		return fmt.Errorf("音视频合并失败: %v", err)
	}

	if err := mc.validateOutput(); err != nil {
		// 如果验证失败，也打印ffmpeg的错误输出
		log.Printf("FFmpeg convert error")
		return err
	}

	log.Printf("MP4转换完成: %s", mc.outputPath)
	//mc.cleanup()
	return nil
}

func convertToMP4WithResolution(inputPath, outputPath string, audioPath string, resolution *Resolution) error {
	converter, err := newMediaConverter(inputPath, outputPath, audioPath, resolution)
	if err != nil {
		return err
	}
	return converter.convert()
}

// 获取Windows设备列表
func getWindowsDevices() ([]Device, error) {
	var devices []Device

	// 获取摄像头设备
	cmd := exec.Command("powershell", "-Command", `
		$OutputEncoding = [Console]::OutputEncoding = [Text.Encoding]::UTF8
		$devices = Get-CimInstance Win32_PnPEntity | Where-Object { $_.PNPClass -eq 'Image' -or $_.PNPClass -eq 'Camera' }
		$devices | ForEach-Object {
			Write-Host ("camera|" + $_.Name + "|" + $_.Name)
		}
	`)
	output, err := cmd.Output()
	if err != nil {
		log.Printf("获取Windows摄像头设备失败: %v", err)
	} else {
		lines := strings.Split(strings.TrimSpace(string(output)), "\n")
		for _, line := range lines {
			parts := strings.Split(strings.TrimSpace(line), "|")
			if len(parts) == 3 {
				device := Device{
					ID:          parts[2],
					Name:        parts[1],
					Type:        "camera",
					IsAvailable: true,
				}
				// 获取摄像头支持的分辨率
				formats, err := getDeviceFormats(device.Name)
				if err != nil {
					log.Printf("获取摄像头格式失败: %v，使用默认格式", err)
					formats = []VideoFormat{
						{
							Resolution:  Resolution{Width: 1280, Height: 720},
							FrameRate:   FrameRate{Num: 30, Den: 1},
							PixelFormat: "yuyv422",
						},
					}
				}
				device.VideoFormats = formats
				if len(formats) > 0 {
					device.CurrentFormat = &formats[0]
				}
				devices = append(devices, device)
			}
		}
	}

	// 获取音频设备
	audioDevices, err := getWindowsAudioDevices()
	if err != nil {
		log.Printf("获取Windows音频设备失败: %v", err)
	}
	devices = append(devices, audioDevices...)

	// 添加屏幕录制设备
	screenDevice := Device{
		ID:          "screen_capture",
		Name:        "屏幕录制",
		Type:        "screen",
		IsAvailable: true,
		VideoFormats: []VideoFormat{
			{
				Resolution: Resolution{Width: 1920, Height: 1080},
				FrameRate:  FrameRate{Num: 30, Den: 1},
			},
			{
				Resolution: Resolution{Width: 1280, Height: 720},
				FrameRate:  FrameRate{Num: 30, Den: 1},
			},
		},
	}
	devices = append(devices, screenDevice)

	return devices, nil
}

func getWindowsAudioDevices() ([]Device, error) {
	var devices []Device

	// 使用ffmpeg获取DirectShow设备列表
	cmd := getFFmpegCmd("-list_devices", "true", "-f", "dshow", "-i", "dummy")
	output, err := cmd.CombinedOutput()
	if err != nil {
		// FFmpeg列出设备时会返回错误，这是预期行为
		log.Printf("FFmpeg列出设备时的预期错误: %v", err)
	}
	outputStr := string(output)
	log.Printf("FFmpeg设备列表输出: %s", outputStr)

	// 解析输出以获取音频设备
	lines := strings.Split(outputStr, "\n")
	var currentDevice struct {
		name            string
		alternativeName string
		isAudio         bool
	}

	for _, line := range lines {
		line = strings.TrimSpace(line)
		// 检查设备名称
		if match := regexp.MustCompile(`"([^"]+)" \((audio|video)\)`).FindStringSubmatch(line); len(match) > 1 {
			if currentDevice.name != "" && currentDevice.alternativeName != "" && currentDevice.isAudio {
				// 添加麦克风设备
				devices = append(devices, Device{
					ID:          currentDevice.name,
					Name:        currentDevice.name,
					Type:        "microphone",
					IsAvailable: true,
				})
				log.Printf("添加麦克风设备: %s (ID: %s)", currentDevice.name, currentDevice.name)

				// 添加系统音频设备
				devices = append(devices, Device{
					ID:          currentDevice.name,
					Name:        currentDevice.name,
					Type:        "system_audio",
					IsAvailable: true,
				})
				log.Printf("添加系统音频设备: %s (ID: %s)", currentDevice.name, currentDevice.name)
			}

			currentDevice.name = match[1]
			currentDevice.isAudio = match[2] == "audio"
			currentDevice.alternativeName = ""
		} else if match := regexp.MustCompile(`Alternative name "([^"]+)"`).FindStringSubmatch(line); len(match) > 1 {
			currentDevice.alternativeName = match[1]
		}
	}

	// 处理最后一个设备
	if currentDevice.name != "" && currentDevice.alternativeName != "" && currentDevice.isAudio {
		// 添加麦克风设备
		devices = append(devices, Device{
			ID:          currentDevice.name,
			Name:        currentDevice.name + " (麦克风)",
			Type:        "microphone",
			IsAvailable: true,
		})
		log.Printf("添加麦克风设备: %s (ID: %s)", currentDevice.name+" (麦克风)", currentDevice.name)

		// 添加系统音频设备
		devices = append(devices, Device{
			ID:          currentDevice.name,
			Name:        currentDevice.name + " (系统音频)",
			Type:        "system_audio",
			IsAvailable: true,
		})
		log.Printf("添加系统音频设备: %s (ID: %s)", currentDevice.name+" (系统音频)", currentDevice.name)
	}

	log.Printf("找到 %d 个DirectShow音频设备", len(devices)/2)
	return devices, nil
}

// Test V4L2 device availability and capabilities
func testV4L2Device(devicePath string) error {
	// 快速检查设备是否可以打开
	file, err := os.OpenFile(devicePath, os.O_RDWR, 0)
	if err != nil {
		return fmt.Errorf("设备无法打开: %v", err)
	}
	file.Close()
	return nil
}

func startAudioCapture(device *Device, doRecording bool) (string, error) {
	if device == nil {
		return "", fmt.Errorf("音频设备为空")
	}

	// 获取设备输入格式和路径
	inputFormat := getDeviceInputFormat(device)
	if inputFormat == "" {
		return "", fmt.Errorf("不支持的音频设备类型: %s", device.Type)
	}

	devicePath := getDevicePath(device)
	if devicePath == "" {
		return "", fmt.Errorf("无法获取音频设备路径: %s", device.Type)
	}

	// 创建临时音频文件
	var tempAudioPath string
	if doRecording {
		tempAudioFile, err := os.CreateTemp("recordings", "temp_audio_*.raw")
		if err != nil {
			return "", fmt.Errorf("创建临时音频文件失败: %v", err)
		}
		tempAudioPath = tempAudioFile.Name()
		tempAudioFile.Close() // 关闭文件，让FFmpeg可以写入
	}

	var stream *ffmpeg.Stream
	switch runtime.GOOS {
	case "linux":
		{
			stream = ffmpeg.Input(devicePath, ffmpeg.KwArgs{
				"f":                 inputFormat,
				"thread_queue_size": 8192,
				"sample_rate":       48000,
				"channels":          2,
				"channel_layout":    "stereo",
				//"audio_buffer_size": 50,
				//"sample_size":       16,
			})
			if doRecording {
				stream = ffmpeg.Output([]*ffmpeg.Stream{stream}, tempAudioPath, ffmpeg.KwArgs{
					"c:a":                         "aac",
					"b:a":                         "192k",
					"ar":                          48000,
					"ac":                          2,
					"filter:a":                    "aresample=async=1000",
					"fflags":                      "+nobuffer+genpts+discardcorrupt",
					"flags":                       "low_delay",
					"probesize":                   32,
					"analyzeduration":             0,
					"buffer_size":                 4096,
					"use_wallclock_as_timestamps": 1,
					"max_interleave_delta":        0,
					"f":                           "mp4",
					"movflags":                    "+delay_moov+default_base_moof+frag_keyframe",
				})
			} else {
				stream = ffmpeg.Output([]*ffmpeg.Stream{stream}, "-", ffmpeg.KwArgs{
					"c:a":                         "libopus",
					"b:a":                         "128k",
					"f":                           "webm",
					"ar":                          48000,
					"ac":                          2,
					"use_wallclock_as_timestamps": 1,
				})
			}
		}
	case "windows":
		{
			// Windows下的音频捕获参数
			stream = ffmpeg.Input(devicePath, ffmpeg.KwArgs{
				"f":                 inputFormat,
				"thread_queue_size": 8192,
				"sample_rate":       48000,
				"channels":          2,
				"channel_layout":    "stereo",
				"audio_buffer_size": 50,
			})
			if doRecording {
				stream = ffmpeg.Output([]*ffmpeg.Stream{stream}, tempAudioPath, ffmpeg.KwArgs{
					"c:a":                         "pcm_s16le", // 使用PCM格式
					"b:a":                         "192k",
					"ar":                          48000,
					"ac":                          2,
					"filter:a":                    "aresample=async=1000",
					"fflags":                      "+nobuffer+genpts+discardcorrupt",
					"flags":                       "low_delay",
					"probesize":                   32,
					"analyzeduration":             0,
					"buffer_size":                 4096,
					"use_wallclock_as_timestamps": 1,
					"max_interleave_delta":        0,
					"f":                           "s16le", // 输出为原始PCM格式
					"movflags":                    "+faststart+frag_keyframe+empty_moov",
				})
			} else {
				stream = ffmpeg.Output([]*ffmpeg.Stream{stream}, "-", ffmpeg.KwArgs{
					"c:a":                         "libopus",
					"b:a":                         "128k",
					"f":                           "webm",
					"ar":                          48000,
					"ac":                          2,
					"movflags":                    "+faststart+frag_keyframe+empty_moov",
					"use_wallclock_as_timestamps": 1,
				})
			}
		}
	}
	stream = stream.OverWriteOutput().ErrorToStdOut().SetFfmpegPath(ffmpegPath)
	cmd := stream.Compile()
	// 获取标准输出管道用于WebSocket传输
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", fmt.Errorf("创建stdout管道失败: %v", err)
	}

	// 启动命令
	if err := cmd.Start(); err != nil {
		os.Remove(tempAudioPath)
		return "", fmt.Errorf("启动音频捕获失败: %v", err)
	}

	// 保存命令引用以便后续停止
	device.cmd = cmd
	// 创建一个FFmpegCmd实例
	ffCmd := addFFmpegCmd(cmd)
	// 监听命令结束
	go func() {
		if err := cmd.Wait(); err != nil {
			log.Printf("音频捕获命令结束: %v", err)
		}
		removeFFmpegCmd(ffCmd)
	}()

	if !doRecording {
		// 读取WebM输出并发送到WebSocket
		go func() {
			defer cmd.Wait()

			// 使用更大的缓冲区来确保完整读取初始化段
			initBuffer := make([]byte, 512*1024)
			totalRead := 0
			isInitSent := false

			// 循环读取直到找到完整的初始化段
			for !isInitSent {
				n, err := stdout.Read(initBuffer[totalRead:])
				if err != nil {
					if err != io.EOF {
						log.Printf("读取初始化段失败: %v", err)
						return
					}
					break
				}
				totalRead += n

				// 确保至少有足够的数据进行解析
				if totalRead < 8 {
					continue
				}

				// 查找EBML头
				ebmlPos := bytes.Index(initBuffer[:totalRead], []byte{0x1A, 0x45, 0xDF, 0xA3})
				if ebmlPos == -1 {
					if totalRead >= len(initBuffer) {
						log.Printf("缓冲区已满但未找到EBML头")
						return
					}
					continue
				}

				// 查找Segment头
				segmentPos := bytes.Index(initBuffer[ebmlPos:totalRead], []byte{0x18, 0x53, 0x80, 0x67})
				if segmentPos == -1 {
					if totalRead >= len(initBuffer) {
						log.Printf("缓冲区已满但未找到Segment头")
						return
					}
					continue
				}
				segmentPos += ebmlPos

				// 查找第一个Cluster头
				clusterPos := bytes.Index(initBuffer[segmentPos:totalRead], []byte{0x1F, 0x43, 0xB6, 0x75})
				if clusterPos == -1 {
					if totalRead >= len(initBuffer) {
						log.Printf("缓冲区已满但未找到Cluster头")
						return
					}
					continue
				}
				clusterPos += segmentPos

				// 查找Info元素
				infoPos := bytes.Index(initBuffer[segmentPos:clusterPos], []byte{0x15, 0x49, 0xA9, 0x66})
				if infoPos == -1 {
					log.Printf("未找到Info元素")
					return
				}
				infoPos += segmentPos

				// 发送初始化段（包括EBML头、Segment头和Info元素）
				if err := sendFrame(device.ID, initBuffer[:infoPos+4], true, false); err != nil {
					log.Printf("发送初始化段失败: %v", err)
					return
				}
				log.Printf("成功发送初始化段，长度: %d", infoPos+4)

				// 等待一小段时间确保初始化段被处理
				time.Sleep(100 * time.Millisecond)

				// 发送第一个Cluster数据
				if err := sendFrame(device.ID, initBuffer[infoPos+4:totalRead], false, false); err != nil {
					log.Printf("发送第一个Cluster失败: %v", err)
					return
				}
				log.Printf("成功发送第一个Cluster，长度: %d", totalRead-(infoPos+4))

				isInitSent = true
			}

			// 继续读取和发送后续数据
			buffer := make([]byte, 512*1024)
			for {
				n, err := stdout.Read(buffer)
				if err != nil {
					return
				}
				if n > 0 {
					if err := sendFrame(device.ID, buffer[:n], false, false); err != nil {
						log.Printf("发送WebM帧失败: %v", err)
						return
					}
				}
			}
		}()
	}

	return tempAudioPath, nil
}

func checkPulseAudio() error {
	cmd := exec.Command("pulseaudio", "--check")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("PulseAudio not running: %v", err)
	}
	return nil
}

// 创建新的录制会话
func newRecordingSession(sessionType string, configs []RecordingConfig) *RecordingSession {
	return &RecordingSession{
		ID:        fmt.Sprintf("session_%d", time.Now().UnixNano()),
		Type:      sessionType,
		Configs:   configs,
		Status:    "ready",
		TempFiles: make(map[string]string),
	}
}

func init() {
	activeSessions = make(map[string]*RecordingSession)
	globalWSManager = NewWebSocketManager()
	globalStreamManager = NewStreamManager(globalWSManager)

	globalWSManager.RegisterHandler("start_recording", func(data []byte) error {
		log.Printf("收到录制请求数据: %s", string(data))
		// 直接解析完整的消息结构
		var config struct {
			Configs []RecordingConfig `json:"configs"`
		}
		if err := json.Unmarshal(data, &config); err != nil {
			log.Printf("解析录制配置失败: %v, 数据: %s", err, string(data))
			return fmt.Errorf("解析录制配置失败: %v", err)
		}

		log.Printf("开始处理录制请求: %+v", config)
		return handleStartRecording(config)
	})

	globalWSManager.RegisterHandler("stop_recording", func(data []byte) error {
		log.Printf("收到停止录制请求数据: %s", string(data))

		var stopMsg struct {
			SessionID string `json:"sessionId"`
		}

		if err := json.Unmarshal(data, &stopMsg); err != nil {
			log.Printf("解析停止录制消息失败: %v, 数据: %s", err, string(data))
			return fmt.Errorf("解析停止录制消息失败: %v", err)
		}

		if stopMsg.SessionID == "" {
			log.Printf("停止录制消息缺少会话ID")
			return fmt.Errorf("停止录制消息缺少会话ID")
		}

		log.Printf("准备停止录制会话: %s", stopMsg.SessionID)
		return handleStopRecording(stopMsg.SessionID, globalWSManager.conn)
	})

	// 注册WebSocket消息处理函数
	globalWSManager.RegisterHandler("start_preview", func(data []byte) error {
		var config = RecordingConfig{}
		if err := json.Unmarshal(data, &config); err != nil {
			log.Printf("解析录制配置失败: %v, 数据: %s", err, string(data))
			return fmt.Errorf("解析录制配置失败: %v", err)
		}
		log.Printf("开始处理预览请求: %+v", config)
		return handleStartPreview(config)
	})

	globalWSManager.RegisterHandler("stop_preview", func(data []byte) error {
		var msg struct {
			SessionID string `json:"sessionId"`
		}
		if err := json.Unmarshal(data, &msg); err != nil {
			return fmt.Errorf("解析停止预览请求失败: %v", err)
		}
		return handleStopPreview(msg.SessionID, globalWSManager.conn)
	})

	globalWSManager.RegisterHandler("get_session_id", func(data []byte) error {
		return handleGetSessionID(globalWSManager.conn)
	})

	switch runtime.GOOS {
	case "linux", "darwin":
		absPath, err := filepath.Abs("./ffmpeg")
		if err != nil {
			panic(err)
		}
		ffmpegPath = absPath
		//absPath, err = filepath.Abs("./ffprobe")
		//if err != nil {
		//	panic(err)
		//}
		//ffprobePath = absPath
	case "windows":
		absPath, err := filepath.Abs("./ffmpeg.exe")
		if err != nil {
			panic(err)
		}
		ffmpegPath = absPath
		//absPath, err = filepath.Abs("./ffprobe.exe")
		//if err != nil {
		//	panic(err)
		//}
		//ffprobePath = absPath
	}
}

// Enhance getAvailableDevices for Linux
func getAvailableDevices() ([]Device, error) {
	var devices []Device

	// 根据操作系统获取视频设备
	switch runtime.GOOS {
	case "linux":
		// 获取视频设备
		videoDevices, err := getV4L2Devices()
		if err != nil {
			log.Printf("警告: 获取视频设备失败: %v", err)
		}
		devices = append(devices, videoDevices...)

		// Linux特有的屏幕捕获逻辑
		// 检查是否有X11显示
		var display string
		var hasX11 bool
		display = os.Getenv("DISPLAY")
		hasX11 = display != ""

		var screenWidth, screenHeight int = 1280, 720 // 默认值
		var hasDRM bool = false

		// 首先尝试使用DRM获取分辨率
		if _, err := os.Stat("/dev/dri/card0"); err == nil {
			cmd := exec.Command("modetest", "-M", "i915")
			output, err := cmd.Output()
			if err == nil {
				lines := strings.Split(string(output), "\n")
				for _, line := range lines {
					if strings.Contains(line, "connected") {
						re := regexp.MustCompile(`(\d+)x(\d+)`)
						matches := re.FindStringSubmatch(line)
						if len(matches) == 3 {
							screenWidth, _ = strconv.Atoi(matches[1])
							screenHeight, _ = strconv.Atoi(matches[2])
							hasDRM = true
							log.Printf("检测到DRM分辨率: %dx%d", screenWidth, screenHeight)
							break
						}
					}
				}
			}
		}

		// 如果DRM不可用,尝试X11
		if !hasDRM && hasX11 {
			cmd := exec.Command("xrandr", "--current")
			output, err := cmd.Output()
			if err == nil {
				lines := strings.Split(string(output), "\n")
				for _, line := range lines {
					if strings.Contains(line, " connected") && strings.Contains(line, "primary") {
						re := regexp.MustCompile(`(\d+)x(\d+)\+\d+\+\d+`)
						matches := re.FindStringSubmatch(line)
						if len(matches) == 3 {
							screenWidth, _ = strconv.Atoi(matches[1])
							screenHeight, _ = strconv.Atoi(matches[2])
							log.Printf("检测到X11分辨率: %dx%d", screenWidth, screenHeight)
							break
						}
					}
				}
			} else {
				log.Printf("警告: 获取X11屏幕分辨率失败: %v", err)
			}
		}

		// 添加Linux屏幕捕获设备
		screenDevice := Device{
			ID:          "screen-capture",
			Name:        "屏幕捕获",
			Type:        DeviceScreen,
			IsAvailable: false, // 默认设置为不可用
			VideoFormats: []VideoFormat{
				{
					Resolution: Resolution{
						Width:  screenWidth,
						Height: screenHeight,
					},
					FrameRate: FrameRate{
						Num: 30,
						Den: 1,
					},
					PixelFormat: "kmsgrab",
				},
				{
					Resolution: Resolution{
						Width:  screenWidth,
						Height: screenHeight,
					},
					FrameRate: FrameRate{
						Num: 30,
						Den: 1,
					},
					PixelFormat: "x11grab",
				},
			},
		}

		// 检查X11可用性并设置设备路径
		if hasX11 {
			screenDevice.IsAvailable = true
			screenDevice.Path = fmt.Sprintf("%s+0,0", display)
			screenDevice.CurrentFormat = &screenDevice.VideoFormats[1] // 使用x11grab格式
			log.Printf("添加X11屏幕捕获设备，路径: %s, 分辨率: %dx%d", screenDevice.Path, screenWidth, screenHeight)
		} else if hasDRM {
			screenDevice.IsAvailable = true
			screenDevice.Path = "/dev/dri/card0"
			screenDevice.CurrentFormat = &screenDevice.VideoFormats[0] // 使用kmsgrab格式
			log.Printf("X11显示不可用，使用DRM设备进行屏幕捕获，分辨率: %dx%d", screenWidth, screenHeight)
		} else {
			log.Printf("警告: 无可用的屏幕捕获设备")
		}
		devices = append(devices, screenDevice)

		// 获取Linux音频设备
		audioDevices, err := getPulseAudioDevices()
		if err != nil {
			log.Printf("警告: 获取音频设备失败: %v", err)
		}
		devices = append(devices, audioDevices...)

	case "windows":
		// 获取Windows设备（包括摄像头和音频设备）
		windowsDevices, err := getWindowsDevices()
		if err != nil {
			log.Printf("警告: 获取Windows设备失败: %v", err)
		}
		devices = append(devices, windowsDevices...)

	case "darwin":
		// TODO: 添加macOS设备获取逻辑
		log.Printf("警告: macOS设备获取尚未实现")
	default:
		log.Printf("警告: 不支持的操作系统: %s", runtime.GOOS)
	}

	// 验证每个设备的可用性
	for i := range devices {
		if err := testDeviceAccess(&devices[i]); err != nil {
			log.Printf("设备 %s (%s) 不可用: %v", devices[i].Name, devices[i].ID, err)
			// 对于屏幕捕获和系统音频，我们总是认为它们是可用的
			if devices[i].Type != DeviceScreen && devices[i].Type != DeviceSystemAudio {
				devices[i].IsAvailable = false
			}
		} else {
			devices[i].IsAvailable = true
			log.Printf("设备 %s (%s) 可用", devices[i].Name, devices[i].ID)
		}
	}

	log.Printf("找到 %d 个设备", len(devices))
	for _, device := range devices {
		log.Printf("设备: %s (%s) - 可用: %v", device.Name, device.Type, device.IsAvailable)
	}

	return devices, nil
}

// 获取V4L2设备
func getV4L2Devices() ([]Device, error) {
	var devices []Device

	// 检查v4l2-ctl是否可用
	if _, err := exec.LookPath("v4l2-ctl"); err == nil {
		// 获取所有视频设备
		cmd := exec.Command("v4l2-ctl", "--list-devices")
		output, err := cmd.CombinedOutput()
		if err == nil {
			lines := strings.Split(string(output), "\n")
			var currentName string
			var devicePaths []string

			// 首先收集同一个物理设备的所有设备路径
			for _, line := range lines {
				if strings.TrimSpace(line) == "" {
					continue
				}
				if !strings.HasPrefix(line, "\t") {
					// 处理之前收集的设备
					if len(devicePaths) > 0 {
						// 测试每个设备路径，找到可用的视频设备
						for _, path := range devicePaths {
							if strings.HasPrefix(path, "/dev/video") {
								if err := testV4L2Device(path); err == nil {
									// 获取设备支持的视频格式
									formats, err := getDeviceFormats(path)
									if err != nil {
										log.Printf("获取设备格式失败 %s: %v", path, err)
										continue
									}

									// 选择默认格式（优先选择1080p@30fps）
									var defaultFormat *VideoFormat
									for i, format := range formats {
										if format.Resolution.Width == 1920 && format.Resolution.Height == 1080 &&
											format.FrameRate.Num == 30 && format.FrameRate.Den == 1 {
											defaultFormat = &formats[i]
											break
										}
									}
									// 如果没有找到1080p，使用第一个可用格式
									if defaultFormat == nil && len(formats) > 0 {
										defaultFormat = &formats[0]
									}

									device := Device{
										ID:            path,
										Name:          currentName,
										Type:          DeviceCamera,
										IsAvailable:   true,
										VideoFormats:  formats,
										CurrentFormat: defaultFormat,
									}
									devices = append(devices, device)
									log.Printf("找到可用摄像头: %s (%s), 支持 %d 种格式", currentName, path, len(formats))
									break // 找到一个可用的视频设备就停止
								}
							}
						}
					}
					// 开始新的设备
					currentName = strings.TrimSpace(line)
					devicePaths = nil
				} else {
					// 收集设备路径
					devicePaths = append(devicePaths, strings.TrimSpace(line))
				}
			}
			// 处理最后一个设备
			if len(devicePaths) > 0 {
				for _, path := range devicePaths {
					if strings.HasPrefix(path, "/dev/video") {
						if err := testV4L2Device(path); err == nil {
							formats, err := getDeviceFormats(path)
							if err != nil {
								log.Printf("获取设备格式失败 %s: %v", path, err)
								continue
							}

							var defaultFormat *VideoFormat
							for i, format := range formats {
								if format.Resolution.Width == 1920 && format.Resolution.Height == 1080 &&
									format.FrameRate.Num == 30 && format.FrameRate.Den == 1 {
									defaultFormat = &formats[i]
									break
								}
							}
							if defaultFormat == nil && len(formats) > 0 {
								defaultFormat = &formats[0]
							}

							device := Device{
								ID:            path,
								Name:          currentName,
								Type:          DeviceCamera,
								IsAvailable:   true,
								VideoFormats:  formats,
								CurrentFormat: defaultFormat,
							}
							devices = append(devices, device)
							log.Printf("找到可用摄像头: %s (%s), 支持 %d 种格式", currentName, path, len(formats))
							break
						}
					}
				}
			}
		}
	}

	return devices, nil
}

// 获取PulseAudio设备
func getPulseAudioDevices() ([]Device, error) {
	var devices []Device

	// 检查PulseAudio是否运行
	if err := checkPulseAudio(); err != nil {
		log.Printf("PulseAudio未运行: %v", err)
		// 尝试使用ALSA设备
		if output, err := exec.Command("arecord", "-l").Output(); err == nil {
			lines := strings.Split(string(output), "\n")
			for _, line := range lines {
				if strings.HasPrefix(line, "card ") {
					fields := strings.Fields(line)
					if len(fields) >= 4 {
						cardNum := strings.TrimPrefix(fields[1], ":")
						deviceName := strings.Join(fields[3:], " ")
						devices = append(devices, Device{
							ID:          fmt.Sprintf("hw:%s,0", cardNum),
							Name:        deviceName,
							Type:        DeviceMicrophone,
							IsAvailable: true,
						})
						log.Printf("找到ALSA音频设备: %s (hw:%s,0)", deviceName, cardNum)
					}
				}
			}
		}
		// 如果没有找到任何设备，添加默认设备
		if len(devices) == 0 {
			devices = append(devices, Device{
				ID:          "default",
				Name:        "默认音频设备",
				Type:        DeviceMicrophone,
				IsAvailable: true,
			})
			log.Printf("添加默认音频设备")
		}
		return devices, nil
	}

	// 获取音频输入设备（麦克风）
	cmd := exec.Command("pactl", "list", "sources")
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("获取音频输入设备失败: %v", err)
	}

	// 解析输出
	sources := strings.Split(string(output), "Source #")
	for _, source := range sources[1:] { // 跳过第一个空白部分
		lines := strings.Split(source, "\n")
		var deviceID, deviceName string
		var isMonitor bool
		var isVirtual bool

		for _, line := range lines {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "Name: ") {
				deviceID = strings.TrimPrefix(line, "Name: ")
				// 检查是否是虚拟设备
				isVirtual = strings.Contains(deviceID, "auto_null") ||
					strings.Contains(deviceID, "dummy")
			} else if strings.HasPrefix(line, "Description: ") {
				deviceName = strings.TrimPrefix(line, "Description: ")
			} else if strings.Contains(line, "Monitor of ") {
				isMonitor = true
			}
		}

		// 跳过虚拟设备
		if isVirtual {
			continue
		}

		if deviceID != "" && deviceName != "" {
			deviceType := DeviceMicrophone
			if isMonitor {
				deviceType = DeviceSystemAudio
			}

			device := Device{
				ID:          deviceID,
				Name:        deviceName,
				Type:        deviceType,
				IsAvailable: true,
			}

			// 测试设备
			if err := testPulseAudioDevice(deviceID); err == nil {
				devices = append(devices, device)
				log.Printf("找到音频设备: %s (%s)", deviceName, deviceID)
			}
		}
	}

	// 如果没有找到任何设备，添加默认设备
	if len(devices) == 0 {
		devices = append(devices, Device{
			ID:          "default",
			Name:        "默认音频设备",
			Type:        DeviceMicrophone,
			IsAvailable: true,
		})
		log.Printf("添加默认音频设备")
	}

	return devices, nil
}

// startMultiStreamRecording starts recording from multiple devices simultaneously
func startMultiStreamRecording(session *RecordingSession) error {
	// 先清理所有临时文件
	if err := cleanupTempFiles(); err != nil {
		log.Printf("警告：清理临时文件失败: %v", err)
	}

	if session == nil {
		return fmt.Errorf("session is nil")
	}

	if len(session.Configs) == 0 {
		return fmt.Errorf("no recording configs provided")
	}

	// 确保通道已初始化
	session.doneMutex.Lock()
	if session.done == nil {
		session.done = make(chan struct{})
	}
	session.doneMutex.Unlock()

	session.streamMutex.Lock()
	if session.streamDone == nil {
		session.streamDone = make(chan struct{})
	}
	session.streamMutex.Unlock()

	// 记录开始时间
	session.StartTime = time.Now()

	// 启动录制
	for i, config := range session.Configs {
		// 确保设备路径已设置
		if config.VideoDevice != nil {
			devicePath := getDevicePath(config.VideoDevice)
			if devicePath == "" {
				return fmt.Errorf("无法获取视频设备路径: %s", config.VideoDevice.Type)
			}
			config.VideoDevice.Path = devicePath
			log.Printf("视频设备路径: %s", devicePath)
		}

		if config.AudioDevice != nil {
			devicePath := getDevicePath(config.AudioDevice)
			if devicePath == "" {
				return fmt.Errorf("无法获取音频设备路径: %s", config.AudioDevice.Type)
			}
			config.AudioDevice.Path = devicePath
			log.Printf("音频设备路径: %s", devicePath)
		}

		// 启动视频捕获
		tempVideoPath, err := startVideoCapture(config.VideoDevice, true)
		if err != nil {
			os.Remove(tempVideoPath)
			return fmt.Errorf("启动视频捕获失败: %v", err)
		}
		session.TempFiles[fmt.Sprintf("video_%d", i)] = tempVideoPath

		// 启动音频捕获
		tempAudioPath, err := startAudioCapture(config.AudioDevice, true)
		if err != nil {
			os.Remove(tempVideoPath)
			os.Remove(tempAudioPath)
			return fmt.Errorf("启动音频捕获失败: %v", err)
		}
		session.TempFiles[fmt.Sprintf("audio_%d", i)] = tempAudioPath
	}

	return nil
}

// 新增函数：直接将视频捕获写入文件
func startVideoCapture(device *Device, doRecording bool) (string, error) {
	if device == nil {
		return "", fmt.Errorf("设备为空")
	}

	if device.CurrentFormat == nil {
		return "", fmt.Errorf("未选择视频格式: %s", device.ID)
	}

	// 获取设备输入格式
	inputFormat := getDeviceInputFormat(device)
	if inputFormat == "" {
		return "", fmt.Errorf("不支持的设备类型: %s", device.Type)
	}

	// 获取设备路径
	devicePath := getDevicePath(device)
	if devicePath == "" {
		return "", fmt.Errorf("设备路径为空")
	}

	// 创建临时视频文件
	var tempVideoPath string
	if doRecording {
		tempVideoFile, err := os.CreateTemp("recordings", "temp_video_*.mp4")
		if err != nil {
			return "", fmt.Errorf("创建临时视频文件失败: %v", err)
		}
		tempVideoPath = tempVideoFile.Name()
		tempVideoFile.Close() // 关闭文件，让FFmpeg可以写入
	}

	// 构建输入参数
	//inputArgs := ffmpeg.KwArgs{
	//	"f":               inputFormat,
	//	"probesize":       "32",
	//	"analyzeduration": "0",
	//	"fflags":          "+nobuffer",
	//}

	//switch device.Type {
	//case DeviceCamera:
	//	inputArgs["video_size"] = fmt.Sprintf("%dx%d",
	//		device.CurrentFormat.Resolution.Width,
	//		device.CurrentFormat.Resolution.Height)
	//	inputArgs["framerate"] = fmt.Sprintf("%d/%d",
	//		device.CurrentFormat.FrameRate.Num,
	//		device.CurrentFormat.FrameRate.Den)
	//case DeviceScreen:
	//	// 屏幕录制参数
	//	switch runtime.GOOS {
	//	case "linux":
	//		// X11grab 特定参数
	//		inputArgs["video_size"] = fmt.Sprintf("%dx%d",
	//			device.CurrentFormat.Resolution.Width,
	//			device.CurrentFormat.Resolution.Height)
	//		inputArgs["framerate"] = 30
	//		inputArgs["draw_mouse"] = "1"
	//		inputArgs["thread_queue_size"] = "4096"
	//		// 可选：指定屏幕偏移
	//		if offset := device.CurrentFormat.Offset; offset != nil {
	//			devicePath = fmt.Sprintf("%s+%d,%d", devicePath, offset.X, offset.Y)
	//		}
	//	case "windows":
	//		// GDIgrab 特定参数
	//		inputArgs["video_size"] = fmt.Sprintf("%dx%d",
	//			device.CurrentFormat.Resolution.Width,
	//			device.CurrentFormat.Resolution.Height)
	//		inputArgs["framerate"] = 30
	//		inputArgs["draw_mouse"] = "1"
	//		inputArgs["offset_x"] = "0"
	//		inputArgs["offset_y"] = "0"
	//		inputArgs["thread_queue_size"] = "4096"
	//	case "darwin":
	//		// AVFoundation 特定参数
	//		inputArgs["capture_cursor"] = "1"
	//		inputArgs["capture_mouse_clicks"] = "1"
	//		inputArgs["framerate"] = 30
	//		inputArgs["video_size"] = fmt.Sprintf("%dx%d",
	//			device.CurrentFormat.Resolution.Width,
	//			device.CurrentFormat.Resolution.Height)
	//		inputArgs["pixel_format"] = "uyvy422"
	//		inputArgs["thread_queue_size"] = "4096"
	//	}
	//}

	// 使用exec.Command构建FFmpeg命令
	args := []string{
		"-f", inputFormat,
		"-probesize", "32",
		"-analyzeduration", "0",
		"-fflags", "+nobuffer",
	}

	// 添加设备特定参数
	switch device.Type {
	case DeviceCamera:
		args = append(args,
			"-video_size", fmt.Sprintf("%dx%d",
				device.CurrentFormat.Resolution.Width,
				device.CurrentFormat.Resolution.Height),
			"-framerate", fmt.Sprintf("%d/%d",
				device.CurrentFormat.FrameRate.Num,
				device.CurrentFormat.FrameRate.Den),
		)

	case DeviceScreen:
		switch runtime.GOOS {
		case "linux":
			args = append(args,
				"-video_size", fmt.Sprintf("%dx%d",
					device.CurrentFormat.Resolution.Width,
					device.CurrentFormat.Resolution.Height),
				"-framerate", "30",
				"-draw_mouse", "1",
				"-thread_queue_size", "4096",
			)
			// 可选：指定屏幕偏移
			if offset := device.CurrentFormat.Offset; offset != nil {
				devicePath = fmt.Sprintf("%s+%d,%d", devicePath, offset.X, offset.Y)
			}
		case "windows":
			args = append(args,
				"-video_size", fmt.Sprintf("%dx%d",
					device.CurrentFormat.Resolution.Width,
					device.CurrentFormat.Resolution.Height),
				"-framerate", "30",
				"-draw_mouse", "1",
				"-offset_x", "0",
				"-offset_y", "0",
				"-thread_queue_size", "4096",
			)
		case "darwin":
			args = append(args,
				"-capture_cursor", "1",
				"-capture_mouse_clicks", "1",
				"-framerate", "30",
				"-video_size", fmt.Sprintf("%dx%d",
					device.CurrentFormat.Resolution.Width,
					device.CurrentFormat.Resolution.Height),
				"-pixel_format", "uyvy422",
				"-thread_queue_size", "4096",
			)
		}
	}

	// 添加输入设备
	args = append(args, "-i", devicePath)

	if doRecording {
		args = append(args,
			//"-use_wallclock_as_timestamps", "1",
			//"-copyts",
			// 本地录制输出
			"-map", "0:v",
			"-c:v", "libx264",
			//"-preset", "ultrafast",
			//"-tune", "zerolatency",
			//"-profile:v", "baseline",
			//"-level", "4.2",
			"-pix_fmt", "yuv420p",
			//"-crf", "23",
			//"-g", "30",
			//"-keyint_min", "16",
			//"-sc_threshold", "0",
			//"-fps_mode", "cfr",
			"-r", "30",
			//"-max_muxing_queue_size", "8192",
			//"-fflags", "+genpts",
			//"-fflags", "+genpts+igndts+discardcorrupt+flush_packets+nobuffer",
			//"-flags", "+low_delay+bitexact",
			//"-movflags", "+faststart+empty_moov",
			//"-movflags", "+write_colr",
			//"-use_wallclock_as_timestamps", "1",
			//"-metadata", "creation_time="+time.Now().UTC().Format(time.RFC3339Nano),
			//"-enc_time_base", "demux",
			//"-vsync", "0",
			//"-vf", "setpts=N/(30*TB)",
			//"-vf", "'settb=AVTB, setpts=N/SR/TB'",
			//"-probesize", "32M",
			//"-analyzeduration", "10M",
			//"-thread_queue_size", "8192",
			//"-copyts",
			//"-start_at_zero",
			//"-avoid_negative_ts", "make_zero",
			//"-max_interleave_delta", "0",
			//"-frag_duration", "1000000",
			//"-min_frag_duration", "1000000",
			//"-write_tmcd", "0",
			//"-flush_packets", "1",
			//"-vf", "settb=AVTB, drawtext=text='%{localtime}': fontsize=24: fontcolor=white: x=10: y=10",
			"-f", "mp4",
			tempVideoPath,
			// 预览输出
			"-map", "0:v",
			"-c:v", "libvpx-vp9",
			"-pix_fmt", "yuv420p",
			"-r", "30",
			//"-b:v", "2M",
			//"-maxrate", "2M",
			//"-bufsize", "4M",
			//"-row-mt", "1",
			//"-tile-columns", "2",
			//"-frame-parallel", "0",
			//"-threads", "2",
			//"-quality", "realtime",
			//"-speed", "8",
			//"-g", "30",
			//"-keyint_min", "15",
			//"-deadline", "realtime",
			//"-cpu-used", "8",
			//"-error-resilient", "1",
			//"-lag-in-frames", "0",
			//"-static-thresh", "0",
			//"-max_muxing_queue_size", "4096",
			//"-cluster_size_limit", "512k",
			//"-cluster_time_limit", "1000",
			//"-live", "1",
			//"-flags", "+global_header",
			"-f", "webm",
			"pipe:1",
			"-y",
		)
	} else {
		// 只添加预览输出
		args = append(args,
			"-c:v", "libvpx-vp9",
			"-pix_fmt", "yuv420p",
			"-r", "10",
			"-b:v", "2M",
			"-maxrate", "2M",
			"-bufsize", "4M",
			"-row-mt", "1",
			"-tile-columns", "2",
			"-frame-parallel", "0",
			"-threads", "2",
			"-quality", "realtime",
			"-speed", "8",
			"-g", "30",
			"-keyint_min", "15",
			"-deadline", "realtime",
			"-cpu-used", "8",
			"-error-resilient", "1",
			"-lag-in-frames", "0",
			"-static-thresh", "0",
			"-max_muxing_queue_size", "4096",
			"-cluster_size_limit", "512k",
			"-cluster_time_limit", "1000",
			"-live", "1",
			"-flags", "+global_header",
			"-f", "webm",
			"pipe:1",
		)
	}

	// 创建命令
	cmd := exec.Command(ffmpegPath, args...)
	cmd.Stderr = os.Stderr
	log.Printf("FFmpeg命令: %s", cmd.String())

	// 获取标准输出管道用于WebSocket传输
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", fmt.Errorf("创建stdout管道失败: %v", err)
	}

	// 启动命令
	if err := cmd.Start(); err != nil {
		os.Remove(tempVideoPath)
		return "", fmt.Errorf("启动视频捕获失败: %v", err)
	}

	// 保存命令引用
	device.cmd = cmd
	// 创建一个FFmpegCmd实例
	ffCmd := addFFmpegCmd(cmd)

	// 监听命令结束
	go func() {
		cmd.Wait()
		removeFFmpegCmd(ffCmd)
		log.Printf("FFmpeg命令已结束 [%s]", device.Type)
	}()

	// 无论预览还是录制都需要传递视频流
	if stdout != nil {
		go handleVideoStream(device, stdout)
	}

	return tempVideoPath, nil
}

func testDeviceAccess(device *Device) error {
	switch runtime.GOOS {
	case "windows":
		switch device.Type {
		case DeviceScreen:
			return nil // Windows平台默认允许屏幕录制
		case DeviceSystemAudio:
			return nil // Windows平台系统音频默认可用
		case DeviceMicrophone:
			// 检查麦克风设备
			deviceID := strings.TrimSuffix(device.ID, "_mic")
			cmd := getFFmpegCmd("-list_devices", "true", "-f", "dshow", "-i", "dummy")
			output, _ := cmd.CombinedOutput()
			if !strings.Contains(string(output), deviceID) {
				return fmt.Errorf("麦克风设备未找到: %s", deviceID)
			}
			return nil
		case DeviceCamera:
			// 检查摄像头设备
			cmd := getFFmpegCmd("-list_devices", "true", "-f", "dshow", "-i", "dummy")
			output, _ := cmd.CombinedOutput()
			if !strings.Contains(string(output), device.ID) {
				return fmt.Errorf("摄像头设备未找到: %s", device.ID)
			}
			return nil
		default:
			return fmt.Errorf("不支持的设备类型: %s", device.Type)
		}
	case "linux":
		switch device.Type {
		case DeviceScreen:
			// 检查是否有X11显示
			display := os.Getenv("DISPLAY")
			if display == "" {
				// 如果没有X11显示，检查是否有framebuffer设备
				if _, err := os.Stat("/dev/fb0"); err != nil {
					return fmt.Errorf("no screen capture device available")
				}
			}
			return nil
		case DeviceSystemAudio:
			return testPulseAudioDevice(device.ID)
		case DeviceMicrophone:
			return testPulseAudioDevice(device.ID)
		case DeviceCamera:
			return testV4L2Device(device.ID)
		default:
			return fmt.Errorf("不支持的设备类型: %s", device.Type)
		}
	case "darwin":
		switch device.Type {
		case DeviceScreen:
			return nil // macOS默认允许屏幕录制
		case DeviceSystemAudio:
			return nil // macOS默认允许系统音频
		case DeviceMicrophone:
			return nil // macOS默认允许麦克风
		case DeviceCamera:
			// 检查摄像头设备
			cmd := getFFmpegCmd("-f", "avfoundation", "-list_devices", "true", "-i", "")
			output, _ := cmd.CombinedOutput()
			if !strings.Contains(string(output), device.ID) {
				return fmt.Errorf("摄像头设备未找到: %s", device.ID)
			}
			return nil
		default:
			return fmt.Errorf("不支持的设备类型: %s", device.Type)
		}
	default:
		return fmt.Errorf("不支持的操作系统: %s", runtime.GOOS)
	}
}

// Test PulseAudio device availability
func testPulseAudioDevice(deviceID string) error {
	// 首先检查PulseAudio是否运行
	if err := exec.Command("pulseaudio", "--check").Run(); err != nil {
		// 尝试启动PulseAudio
		log.Printf("PulseAudio未运行，尝试启动...")
		startCmd := exec.Command("pulseaudio", "--start", "--log-target=syslog")
		if err := startCmd.Run(); err != nil {
			return fmt.Errorf("启动PulseAudio失败: %v", err)
		}
		// 等待PulseAudio初始化
		time.Sleep(time.Second)
	}

	// 如果是默认设备，直接返回成功
	if deviceID == "default" {
		return nil
	}

	// 检查设备是否存在
	cmd := exec.Command("pactl", "list")
	output, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("获取PulseAudio设备列表失败: %v", err)
	}

	// 检查设备ID是否在输出中
	if !strings.Contains(string(output), deviceID) {
		return fmt.Errorf("未找到音频设备: %s", deviceID)
	}

	return nil
}

func (session *RecordingSession) Stop() {
	// 关闭通道，通知所有goroutine停止
	session.doneMutex.Lock()
	if session.done != nil {
		close(session.done)
		session.done = nil
	}
	session.doneMutex.Unlock()

	session.streamMutex.Lock()
	if session.streamDone != nil {
		close(session.streamDone)
		session.streamDone = nil
	}
	session.streamMutex.Unlock()

	// 创建等待组来等待所有进程退出
	var wg sync.WaitGroup

	// 停止所有录制进程
	for _, config := range session.Configs {
		if config.VideoDevice != nil && config.VideoDevice.cmd != nil {
			wg.Add(1)
			go func(cmd *exec.Cmd) {
				defer wg.Done()
				if cmd != nil && cmd.Process != nil {
					if err := cmd.Process.Signal(syscall.SIGINT); err != nil {
						log.Printf("停止视频捕获失败: %v", err)
					}
					cmd.Wait()
				}
			}(config.VideoDevice.cmd)
		}

		if config.AudioDevice != nil && config.AudioDevice.cmd != nil {
			wg.Add(1)
			go func(cmd *exec.Cmd) {
				defer wg.Done()
				if cmd != nil && cmd.Process != nil {
					if err := cmd.Process.Signal(syscall.SIGINT); err != nil {
						log.Printf("停止音频捕获失败: %v", err)
					}
					cmd.Wait()
				}
			}(config.AudioDevice.cmd)
		}
	}

	// 等待所有进程退出，最多等待5秒
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		log.Printf("所有录制进程已正常退出")
	case <-time.After(5 * time.Second):
		log.Printf("等待进程退出超时，强制结束")
		for _, config := range session.Configs {
			if config.VideoDevice != nil && config.VideoDevice.cmd != nil && config.VideoDevice.cmd.Process != nil {
				config.VideoDevice.cmd.Process.Kill()
			}
			if config.AudioDevice != nil && config.AudioDevice.cmd != nil && config.AudioDevice.cmd.Process != nil {
				config.AudioDevice.cmd.Process.Kill()
			}
		}
	}

	// 清理设备状态
	for _, config := range session.Configs {
		if config.VideoDevice != nil {
			config.VideoDevice.cmd = nil
		}
		if config.AudioDevice != nil {
			config.AudioDevice.cmd = nil
		}
	}
}

func sendFrame(deviceID string, frame []byte, isInit bool, isVideo bool) error {
	return globalStreamManager.ProcessFrame(deviceID, frame, isInit, isVideo)
}

// 获取设备输入格式
func getDeviceInputFormat(device *Device) string {
	if device == nil {
		return ""
	}

	switch device.Type {
	case DeviceCamera:
		switch runtime.GOOS {
		case "linux":
			return "v4l2"
		case "windows":
			return "dshow"
		case "darwin":
			return "avfoundation"
		}
	case DeviceScreen:
		switch runtime.GOOS {
		case "linux":
			return "x11grab"
		case "windows":
			return "gdigrab"
		case "darwin":
			return "avfoundation"
		}
	case DeviceMicrophone, DeviceSystemAudio:
		switch runtime.GOOS {
		case "linux":
			return "pulse"
		case "windows":
			return "dshow"
		case "darwin":
			return "avfoundation"
		}
	}
	return ""
}

// 获取设备路径
func getDevicePath(device *Device) string {
	if device == nil {
		return ""
	}

	switch runtime.GOOS {
	case "linux":
		switch device.Type {
		case DeviceCamera:
			return device.ID // 使用设备ID作为路径 (例如 /dev/video0)
		case DeviceScreen:
			return ":0.0"
		case DeviceMicrophone:
			if device.ID == "default" {
				return "default"
			}
			// 确保设备ID格式正确
			if strings.HasPrefix(device.ID, "alsa_input.") {
				return device.ID
			}
			return fmt.Sprintf("alsa_input.%s", device.ID)
		case DeviceSystemAudio:
			// 获取默认的PulseAudio监视器源
			cmd := exec.Command("pactl", "list", "sinks")
			output, err := cmd.Output()
			if err == nil {
				// 解析输出找到默认sink
				lines := strings.Split(string(output), "\n")
				var sinkName string
				for _, line := range lines {
					if strings.Contains(line, "Name:") {
						sinkName = strings.TrimSpace(strings.TrimPrefix(line, "Name:"))
						break
					}
				}
				if sinkName != "" {
					return sinkName + ".monitor"
				}
			}
			// 如果无法获取默认sink，使用备用设备
			return "alsa_output.pci-0000_00_1f.3.analog-stereo.monitor"
		}
	case "windows":
		switch device.Type {
		case DeviceCamera:
			return fmt.Sprintf("video=%s", device.Name) // Windows dshow格式
		case DeviceScreen:
			return "desktop"
		case DeviceMicrophone, DeviceSystemAudio:
			// 移除设备类型后缀
			deviceName := strings.TrimSuffix(strings.TrimSuffix(device.Name, " (麦克风)"), " (系统音频)")
			return fmt.Sprintf("audio=%s", deviceName)
		}
	case "darwin":
		switch device.Type {
		case DeviceCamera:
			return device.ID
		case DeviceScreen:
			return "1"
		case DeviceMicrophone, DeviceSystemAudio:
			return device.ID
		}
	}
	return ""
}

// 检查并清理指定端口
func cleanupPort(port int) error {
	if runtime.GOOS == "windows" {
		// Windows下使用netstat命令
		cmd := exec.Command("netstat", "-ano", "|", "findstr", fmt.Sprintf(":%d", port))
		output, err := cmd.CombinedOutput()
		if err != nil {
			// 如果命令执行失败，可能是因为端口未被占用
			return nil
		}

		// 解析输出找到PID
		lines := strings.Split(string(output), "\n")
		for _, line := range lines {
			if strings.Contains(line, fmt.Sprintf(":%d", port)) {
				fields := strings.Fields(line)
				if len(fields) > 4 {
					pid := fields[4]
					if pidNum, err := strconv.Atoi(pid); err == nil {
						// 尝试终止进程
						proc, err := os.FindProcess(pidNum)
						if err == nil {
							log.Printf("正在终止占用端口 %d 的进程 (PID: %d)", port, pidNum)
							if err := proc.Kill(); err != nil {
								return fmt.Errorf("终止进程失败: %v", err)
							}
							// 等待进程退出
							time.Sleep(time.Second)
						}
					}
				}
			}
		}
	} else {
		// Linux/Unix系统使用lsof命令
		cmd := exec.Command("lsof", "-i", fmt.Sprintf(":%d", port))
		output, err := cmd.CombinedOutput()
		if err != nil {
			// 如果命令执行失败，可能是因为端口未被占用
			if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
				return nil // 端口未被占用，可以继续
			}
			return fmt.Errorf("检查端口使用失败: %v", err)
		}

		// 解析输出找到PID
		lines := strings.Split(string(output), "\n")
		for i, line := range lines {
			if i == 0 || len(line) == 0 {
				continue // 跳过标题行和空行
			}
			fields := strings.Fields(line)
			if len(fields) > 1 {
				pid := fields[1]
				if pidNum, err := strconv.Atoi(pid); err == nil {
					// 尝试终止进程
					proc, err := os.FindProcess(pidNum)
					if err == nil {
						log.Printf("正在终止占用端口 %d 的进程 (PID: %d)", port, pidNum)
						if err := proc.Signal(syscall.SIGTERM); err != nil {
							log.Printf("使用 SIGTERM 终止进程失败: %v，尝试 SIGKILL", err)
							if err := proc.Kill(); err != nil {
								return fmt.Errorf("终止进程失败: %v", err)
							}
						}
						// 等待进程退出
						time.Sleep(time.Second)
					}
				}
			}
		}
	}

	// 再次检查端口是否已经释放
	time.Sleep(time.Second) // 等待端口完全释放
	if runtime.GOOS == "windows" {
		cmd := exec.Command("netstat", "-ano", "|", "findstr", fmt.Sprintf(":%d", port))
		if output, err := cmd.CombinedOutput(); err == nil && len(output) > 0 {
			return fmt.Errorf("端口 %d 仍然被占用", port)
		}
	} else {
		cmd := exec.Command("lsof", "-i", fmt.Sprintf(":%d", port))
		if output, err := cmd.CombinedOutput(); err == nil && len(output) > 0 {
			return fmt.Errorf("端口 %d 仍然被占用", port)
		}
	}

	return nil
}

// 添加设备管理函数
func releaseVideoDevice(devicePath string) error {
	if devicePath == "" {
		return fmt.Errorf("设备路径为空")
	}

	if runtime.GOOS == "windows" {
		// Windows平台使用tasklist和taskkill命令
		// 查找使用该设备的进程
		cmd := exec.Command("tasklist", "/FI", "IMAGENAME eq ffmpeg.exe", "/FO", "CSV")
		output, err := cmd.CombinedOutput()
		if err != nil {
			return nil // 如果命令失败，可能是因为没有ffmpeg进程
		}

		// 解析CSV输出，获取PID
		lines := strings.Split(string(output), "\n")
		for i, line := range lines {
			if i == 0 { // 跳过标题行
				continue
			}
			fields := strings.Split(line, ",")
			if len(fields) >= 2 {
				// 去除引号
				pid := strings.Trim(fields[1], "\"")
				if pid != "" {
					// 使用taskkill强制终止进程及其子进程
					killCmd := exec.Command("taskkill", "/F", "/T", "/PID", pid)
					if err := killCmd.Run(); err == nil {
						log.Printf("成功终止进程: %s", pid)
					}
				}
			}
		}

		// 等待设备完全释放
		time.Sleep(time.Second)
	} else {
		// Linux平台使用fuser和kill命令
		cmd := exec.Command("fuser", devicePath)
		output, err := cmd.CombinedOutput()
		if err != nil {
			// 如果fuser命令失败，可能是因为没有进程使用该设备
			if len(output) == 0 {
				return nil
			}
			// 尝试使用lsof命令
			cmd = exec.Command("lsof", devicePath)
			output, err = cmd.CombinedOutput()
			if err != nil {
				if len(output) == 0 {
					return nil // 没有进程使用该设备
				}
			}
		}

		// 获取进程ID列表
		pids := strings.Fields(string(output))
		for _, pid := range pids {
			// 检查进程是否是ffmpeg或v4l2相关进程
			cmdlinePath := fmt.Sprintf("/proc/%s/cmdline", pid)
			if cmdline, err := os.ReadFile(cmdlinePath); err == nil {
				cmdlineStr := string(cmdline)
				if strings.Contains(cmdlineStr, "ffmpeg") || strings.Contains(cmdlineStr, "v4l2") {
					// 尝试先用SIGTERM优雅终止
					if pidNum, err := strconv.Atoi(pid); err == nil {
						if proc, err := os.FindProcess(pidNum); err == nil {
							log.Printf("正在终止使用设备 %s 的进程 (PID: %d)", devicePath, pidNum)
							proc.Signal(syscall.SIGINT)

							// 等待进程退出
							time.Sleep(100 * time.Millisecond)

							// 检查进程是否还在运行
							if err := proc.Signal(syscall.Signal(0)); err == nil {
								// 进程还在运行，使用SIGKILL强制终止
								log.Printf("进程未响应SIGTERM，使用SIGKILL强制终止 (PID: %d)", pidNum)
								proc.Signal(syscall.SIGKILL)
								time.Sleep(100 * time.Millisecond)
							}
						}
					}
				}
			}
		}

		// 最后检查设备是否可用
		if err := testV4L2Device(devicePath); err != nil {
			return fmt.Errorf("设备仍然被占用: %v", err)
		}
	}
	return nil
}

// DeviceManager 负责设备管理
type DeviceManager struct {
	mu              sync.RWMutex
	devices         map[string]*Device
	cleanupFuncs    map[string]context.CancelFunc
	globalWSManager *WebSocketManager
	statusTicker    *time.Ticker
	ctx             context.Context
	cancel          context.CancelFunc
}

// 创建新的设备管理器
func NewDeviceManager(globalWSManager *WebSocketManager) *DeviceManager {
	ctx, cancel := context.WithCancel(context.Background())
	dm := &DeviceManager{
		devices:         make(map[string]*Device),
		cleanupFuncs:    make(map[string]context.CancelFunc),
		globalWSManager: globalWSManager,
		ctx:             ctx,
		cancel:          cancel,
	}

	// 启动设备状态监控
	dm.startDeviceMonitoring()
	return dm
}

func (dm *DeviceManager) startDeviceMonitoring() {
	dm.statusTicker = time.NewTicker(2 * time.Second)
	go func() {
		for {
			select {
			case <-dm.ctx.Done():
				dm.statusTicker.Stop()
				return
			case <-dm.statusTicker.C:
				dm.updateDeviceStatus()
			}
		}
	}()
}

func (dm *DeviceManager) updateDeviceStatus() {
	dm.mu.Lock()
	defer dm.mu.Unlock()

	for id, device := range dm.devices {
		prevStatus := device.IsAvailable
		var err error

		switch device.Type {
		case DeviceCamera:
			err = dm.checkCameraAvailability(device)
		case DeviceScreen:
			err = dm.checkScreenAvailability()
		case DeviceMicrophone, DeviceSystemAudio:
			err = dm.checkAudioAvailability(device)
		}

		device.IsAvailable = err == nil

		// 如果状态发生变化，通知前端
		if prevStatus != device.IsAvailable {
			dm.notifyDeviceStatusChange(id, device)
		}
	}
}

func (dm *DeviceManager) notifyDeviceStatusChange(deviceID string, device *Device) {
	if dm.globalWSManager == nil {
		return
	}

	statusMsg := struct {
		Type    string `json:"type"`
		Payload struct {
			DeviceID string `json:"deviceId"`
			Status   string `json:"status"`
		} `json:"payload"`
	}{
		Type: "device_status",
		Payload: struct {
			DeviceID string `json:"deviceId"`
			Status   string `json:"status"`
		}{
			DeviceID: deviceID,
			Status:   map[bool]string{true: "available", false: "unavailable"}[device.IsAvailable],
		},
	}

	if err := dm.globalWSManager.SendJSON(statusMsg); err != nil {
		log.Printf("发送设备状态更新失败: %v", err)
	}
}

// 检查设备可用性
func (dm *DeviceManager) CheckDeviceAvailability(device *Device) error {
	if device == nil {
		return fmt.Errorf("设备为空")
	}

	switch device.Type {
	case DeviceCamera:
		return dm.checkCameraAvailability(device)
	case DeviceScreen:
		return dm.checkScreenAvailability()
	case DeviceMicrophone, DeviceSystemAudio:
		return dm.checkAudioAvailability(device)
	default:
		return fmt.Errorf("不支持的设备类型: %s", device.Type)
	}
}

// 检查摄像头可用性
func (dm *DeviceManager) checkCameraAvailability(device *Device) error {
	switch runtime.GOOS {
	case "linux":
		return dm.checkV4L2Device(device.ID)
	case "windows":
		return dm.checkWindowsCamera(device.Name)
	case "darwin":
		return dm.checkDarwinCamera(device.ID)
	default:
		return fmt.Errorf("不支持的操作系统: %s", runtime.GOOS)
	}
}

// 检查屏幕捕获可用性
func (dm *DeviceManager) checkScreenAvailability() error {
	switch runtime.GOOS {
	case "linux":
		// 检查X11显示
		display := os.Getenv("DISPLAY")
		if display != "" {
			// 如果有X11显示，检查是否可以访问
			if err := exec.Command("xdpyinfo").Run(); err == nil {
				return nil
			}
		}
		// 如果没有X11显示或X11不可访问，检查framebuffer设备
		if _, err := os.Stat("/dev/fb0"); err == nil {
			return nil
		}
		return fmt.Errorf("no screen capture device available")
	case "windows":
		// Windows屏幕捕获检查
		// 使用ffmpeg检查gdigrab是否可用
		cmd := getFFmpegCmd(
			"-f", "gdigrab",
			"-i", "desktop",
			"-t", "0.1",
			"-f", "null",
			"-y",
			"-hide_banner",
			"-loglevel", "error",
			"-",
		)
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("屏幕捕获设备不可用: %v", err)
		}
		return nil
	case "darwin":
		// macOS屏幕捕获检查
		return nil // macOS默认允许屏幕捕获
	default:
		return fmt.Errorf("不支持的操作系统: %s", runtime.GOOS)
	}
}

// 检查音频设备可用性
func (dm *DeviceManager) checkAudioAvailability(device *Device) error {
	switch runtime.GOOS {
	case "linux":
		if device.Type == DeviceSystemAudio {
			return dm.checkPulseAudioMonitor(device.ID)
		}
		return dm.checkPulseAudioDevice(device.ID)
	case "windows":
		return dm.checkWindowsAudio(device.ID)
	case "darwin":
		return dm.checkDarwinAudio(device.ID)
	default:
		return fmt.Errorf("不支持的操作系统: %s", runtime.GOOS)
	}
}

// WebSocketManager 负责管理WebSocket连接和消息处理
type WebSocketManager struct {
	mu       sync.RWMutex
	conn     *websocket.Conn
	writeMu  sync.Mutex
	handlers map[string]func([]byte) error
}

// 创建新的WebSocket管理器
func NewWebSocketManager() *WebSocketManager {
	return &WebSocketManager{
		handlers: make(map[string]func([]byte) error),
	}
}

// 设置WebSocket连接
func (wm *WebSocketManager) SetConnection(conn *websocket.Conn) {
	wm.mu.Lock()
	defer wm.mu.Unlock()
	if wm.conn != nil {
		wm.conn.Close()
	}
	wm.conn = conn
}

// 注册消息处理器
func (wm *WebSocketManager) RegisterHandler(messageType string, handler func([]byte) error) {
	wm.mu.Lock()
	defer wm.mu.Unlock()
	wm.handlers[messageType] = handler
}

// 处理消息
func (wm *WebSocketManager) handleMessage(messageType string, data []byte) error {
	wm.mu.RLock()
	handler, exists := wm.handlers[messageType]
	wm.mu.RUnlock()

	if !exists {
		return fmt.Errorf("未找到消息类型 %s 的处理器", messageType)
	}

	return handler(data)
}

// 发送JSON消息
func (wm *WebSocketManager) SendJSON(msg interface{}) error {
	wm.mu.RLock()
	conn := wm.conn
	wm.mu.RUnlock()

	if conn == nil {
		return fmt.Errorf("WebSocket连接未建立")
	}

	wm.writeMu.Lock()
	defer wm.writeMu.Unlock()
	return conn.WriteJSON(msg)
}

// 发送视频帧
func (wm *WebSocketManager) SendFrame(deviceID string, frame []byte) error {
	wm.mu.RLock()
	conn := wm.conn
	wm.mu.RUnlock()

	if conn == nil {
		return fmt.Errorf("WebSocket连接未建立")
	}

	// 检查帧大小
	if len(frame) > 10*1024*1024 { // 10MB
		return fmt.Errorf("帧大小超过限制: %d bytes", len(frame))
	}

	wm.writeMu.Lock()
	defer wm.writeMu.Unlock()
	return conn.WriteMessage(websocket.BinaryMessage, frame)
}

// 关闭WebSocket管理器
func (wm *WebSocketManager) Close() {
	wm.mu.Lock()
	defer wm.mu.Unlock()

	if wm.conn != nil {
		wm.conn.Close()
		wm.conn = nil
	}
}

// FFmpegConfig 存储FFmpeg配置参数
type FFmpegConfig struct {
	InputFormat string
	VideoCodec  string
	AudioCodec  string
	PixelFormat string
	Framerate   string
	AudioRate   string
	ExtraParams []string
}

// ConfigManager 负责管理不同平台的配置
type ConfigManager struct {
	mu      sync.RWMutex
	configs map[string]map[DeviceType]*FFmpegConfig
}

// 创建新的配置管理器
func NewConfigManager() *ConfigManager {
	cm := &ConfigManager{
		configs: make(map[string]map[DeviceType]*FFmpegConfig),
	}
	cm.initDefaultConfigs()
	return cm
}

// 初始化默认配置
func (cm *ConfigManager) initDefaultConfigs() {
	// Linux配置
	linuxConfigs := make(map[DeviceType]*FFmpegConfig)
	linuxConfigs[DeviceCamera] = &FFmpegConfig{
		InputFormat: "v4l2",
		VideoCodec:  "libvpx-vp9",
		PixelFormat: "yuv420p",
		Framerate:   "30",
		ExtraParams: []string{"-deadline", "realtime", "-cpu-used", "8", "-auto-alt-ref", "0", "-lag-in-frames", "0"},
	}
	linuxConfigs[DeviceScreen] = &FFmpegConfig{
		InputFormat: "x11grab",
		VideoCodec:  "libvpx-vp9",
		PixelFormat: "yuv420p",
		Framerate:   "30",
		ExtraParams: []string{"-deadline", "realtime", "-cpu-used", "8", "-auto-alt-ref", "0", "-lag-in-frames", "0"},
	}
	linuxConfigs[DeviceMicrophone] = &FFmpegConfig{
		InputFormat: "pulse",
		AudioCodec:  "aac",
		AudioRate:   "48000",
		ExtraParams: []string{"-ac", "2", "-strict", "experimental"},
	}
	linuxConfigs[DeviceSystemAudio] = &FFmpegConfig{
		InputFormat: "pulse",
		AudioCodec:  "aac",
		AudioRate:   "48000",
		ExtraParams: []string{"-ac", "2", "-strict", "experimental"},
	}
	cm.configs["linux"] = linuxConfigs

	// Windows配置
	windowsConfigs := make(map[DeviceType]*FFmpegConfig)
	windowsConfigs[DeviceCamera] = &FFmpegConfig{
		InputFormat: "dshow",
		VideoCodec:  "libvpx-vp9",
		PixelFormat: "yuv420p",
		Framerate:   "30",
		ExtraParams: []string{"-deadline", "realtime", "-cpu-used", "8", "-auto-alt-ref", "0", "-lag-in-frames", "0"},
	}
	windowsConfigs[DeviceScreen] = &FFmpegConfig{
		InputFormat: "gdigrab",
		VideoCodec:  "libvpx-vp9",
		PixelFormat: "yuv420p",
		Framerate:   "30",
		ExtraParams: []string{"-deadline", "realtime", "-cpu-used", "8", "-auto-alt-ref", "0", "-lag-in-frames", "0"},
	}
	windowsConfigs[DeviceMicrophone] = &FFmpegConfig{
		InputFormat: "dshow",
		AudioCodec:  "aac",
		AudioRate:   "48000",
		ExtraParams: []string{"-ac", "2"},
	}
	windowsConfigs[DeviceSystemAudio] = &FFmpegConfig{
		InputFormat: "dshow",
		AudioCodec:  "aac",
		AudioRate:   "48000",
		ExtraParams: []string{"-ac", "2"},
	}
	cm.configs["windows"] = windowsConfigs

	// macOS配置
	darwinConfigs := make(map[DeviceType]*FFmpegConfig)
	darwinConfigs[DeviceCamera] = &FFmpegConfig{
		InputFormat: "avfoundation",
		VideoCodec:  "libvpx-vp9",
		PixelFormat: "yuv420p",
		Framerate:   "30",
		ExtraParams: []string{"-deadline", "realtime", "-cpu-used", "8", "-auto-alt-ref", "0", "-lag-in-frames", "0"},
	}
	darwinConfigs[DeviceScreen] = &FFmpegConfig{
		InputFormat: "avfoundation",
		VideoCodec:  "libvpx-vp9",
		PixelFormat: "yuv420p",
		Framerate:   "30",
		ExtraParams: []string{"-deadline", "realtime", "-cpu-used", "8", "-auto-alt-ref", "0", "-lag-in-frames", "0"},
	}
	darwinConfigs[DeviceMicrophone] = &FFmpegConfig{
		InputFormat: "avfoundation",
		AudioCodec:  "aac",
		AudioRate:   "48000",
		ExtraParams: []string{"-ac", "2"},
	}
	darwinConfigs[DeviceSystemAudio] = &FFmpegConfig{
		InputFormat: "avfoundation",
		AudioCodec:  "aac",
		AudioRate:   "48000",
		ExtraParams: []string{"-ac", "2"},
	}
	cm.configs["darwin"] = darwinConfigs
}

// 获取设备配置
func (cm *ConfigManager) GetConfig(deviceType DeviceType) *FFmpegConfig {
	cm.mu.RLock()
	defer cm.mu.RUnlock()

	os := runtime.GOOS
	if configs, ok := cm.configs[os]; ok {
		if config, ok := configs[deviceType]; ok {
			return config
		}
	}
	return nil
}

// 更新设备配置
func (cm *ConfigManager) UpdateConfig(os string, deviceType DeviceType, config *FFmpegConfig) {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	if _, ok := cm.configs[os]; !ok {
		cm.configs[os] = make(map[DeviceType]*FFmpegConfig)
	}
	cm.configs[os][deviceType] = config
}

// 检查V4L2设备
func (dm *DeviceManager) checkV4L2Device(devicePath string) error {
	// 尝试打开设备
	cmd := exec.Command("v4l2-ctl", "--device", devicePath, "--all")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("设备测试失败: %v", err)
	}

	// 检查是否支持视频捕获
	if !strings.Contains(string(output), "Video Capture") {
		return fmt.Errorf("设备不支持视频捕获")
	}

	// 检查是否支持MJPEG或YUYV格式
	formatCmd := exec.Command("v4l2-ctl", "--device", devicePath, "--list-formats")
	formatOutput, err := formatCmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("获取设备格式失败: %v", err)
	}

	formatStr := string(formatOutput)
	if !strings.Contains(formatStr, "MJPG") && !strings.Contains(formatStr, "YUYV") {
		return fmt.Errorf("设备不支持MJPEG或YUYV格式")
	}

	return nil
}

// 检查Windows摄像头
func (dm *DeviceManager) checkWindowsCamera(deviceName string) error {
	// 使用ffmpeg列出DirectShow设备
	cmd := getFFmpegCmd("-list_devices", "true", "-f", "dshow", "-i", "dummy")
	output, err := cmd.CombinedOutput()
	if err != nil {
		// FFmpeg在列出设备时总是返回错误，所以这里不处理错误
		log.Printf("列出设备时的预期错误: %v", err)
	}
	outputStr := string(output)

	// 使用正则表达式精确匹配设备名称
	escapedName := regexp.QuoteMeta(deviceName)
	re := regexp.MustCompile(fmt.Sprintf(`"([^"]*%s[^"]*)"`, escapedName))
	if !re.MatchString(outputStr) {
		return fmt.Errorf("摄像头设备未找到: %s", deviceName)
	}

	// 尝试打开设备
	testCmd := getFFmpegCmd(
		"-f", "dshow",
		"-i", fmt.Sprintf("video=%s", deviceName),
		"-t", "0.1",
		"-f", "null",
		"-y",
		"-hide_banner",
		"-loglevel", "error",
		"NUL",
	)
	if err := testCmd.Run(); err != nil {
		log.Printf("测试摄像头设备失败: %v", err)
		return fmt.Errorf("摄像头设备不可用: %v", err)
	}

	return nil
}

// 检查macOS摄像头
func (dm *DeviceManager) checkDarwinCamera(deviceID string) error {
	// 使用ffmpeg列出AVFoundation设备
	cmd := getFFmpegCmd("-f", "avfoundation", "-list_devices", "true", "-i", "")
	output, _ := cmd.CombinedOutput()
	outputStr := string(output)

	// 检查设备是否在列表中
	if !strings.Contains(outputStr, deviceID) {
		return fmt.Errorf("摄像头设备未找到: %s", deviceID)
	}

	// 尝试打开设备
	testCmd := getFFmpegCmd(
		"-f", "avfoundation",
		"-i", fmt.Sprintf(":%s", deviceID), // macOS设备需要加冒号前缀
		"-t", "0.1",
		"-f", "null",
		"-y",
		"-hide_banner",
		"-loglevel", "error",
		"-",
	)
	if err := testCmd.Run(); err != nil {
		return fmt.Errorf("摄像头设备不可用: %v", err)
	}

	return nil
}

// 检查PulseAudio监视器
func (dm *DeviceManager) checkPulseAudioMonitor(deviceID string) error {
	// 首先检查PulseAudio是否运行
	if err := exec.Command("pulseaudio", "--check").Run(); err != nil {
		// 尝试启动PulseAudio
		startCmd := exec.Command("pulseaudio", "--start", "--log-target=syslog")
		if err := startCmd.Run(); err != nil {
			return fmt.Errorf("启动PulseAudio失败: %v", err)
		}
		// 等待PulseAudio初始化
		time.Sleep(time.Second)
	}

	// 检查监视器源是否存在
	cmd := exec.Command("pactl", "list", "sources")
	output, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("获取音频源列表失败: %v", err)
	}

	if !strings.Contains(string(output), deviceID) {
		return fmt.Errorf("未找到音频监视器: %s", deviceID)
	}

	return nil
}

// 检查PulseAudio设备
func (dm *DeviceManager) checkPulseAudioDevice(deviceID string) error {
	// 如果是默认设备，直接返回成功
	if deviceID == "default" {
		return nil
	}

	// 检查设备是否存在
	cmd := exec.Command("pactl", "list")
	output, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("获取PulseAudio设备列表失败: %v", err)
	}

	// 检查设备ID是否在输出中
	if !strings.Contains(string(output), deviceID) {
		return fmt.Errorf("未找到音频设备: %s", deviceID)
	}

	return nil
}

// 检查Windows音频设备
func (dm *DeviceManager) checkWindowsAudio(deviceID string) error {
	// 从设备ID中移除后缀
	deviceName := strings.TrimSuffix(strings.TrimSuffix(deviceID, " (麦克风)"), " (系统音频)")
	isMic := strings.HasSuffix(deviceID, "(麦克风)")

	// 使用ffmpeg列出DirectShow设备
	cmd := getFFmpegCmd("-list_devices", "true", "-f", "dshow", "-i", "dummy")
	output, err := cmd.CombinedOutput()
	if err != nil {
		// FFmpeg在列出设备时总是返回错误，所以这里不处理错误
		log.Printf("列出设备时的预期错误: %v", err)
	}
	outputStr := string(output)
	log.Printf("检查音频设备 %s 的可用性，设备类型: %s", deviceName, map[bool]string{true: "麦克风", false: "系统音频"}[isMic])

	// 查找音频设备
	var found bool
	lines := strings.Split(outputStr, "\n")
	inAudioSection := false
	for _, line := range lines {
		line = strings.TrimSpace(line)

		if strings.Contains(line, "DirectShow audio devices") {
			inAudioSection = true
			continue
		} else if strings.Contains(line, "DirectShow video devices") {
			inAudioSection = false
			continue
		}

		if !inAudioSection {
			continue
		}

		if match := regexp.MustCompile(`"([^"]+)"`).FindStringSubmatch(line); len(match) > 1 {
			if match[1] == deviceName {
				found = true
				break
			}
		}
	}

	if !found {
		return fmt.Errorf("音频设备未找到: %s", deviceID)
	}

	// 如果是系统音频设备，不需要进一步测试
	if !isMic {
		return nil
	}

	// 测试麦克风设备
	testCmd := getFFmpegCmd(
		"-f", "dshow",
		"-i", fmt.Sprintf("audio=%s", deviceName),
		"-t", "0.1",
		"-f", "null",
		"-y",
		"-hide_banner",
		"-loglevel", "error",
		"NUL",
	)

	if err := testCmd.Run(); err != nil {
		log.Printf("测试音频设备失败: %v", err)
		return fmt.Errorf("音频设备不可用: %v", err)
	}

	return nil
}

// 检查macOS音频设备
func (dm *DeviceManager) checkDarwinAudio(deviceID string) error {
	// 使用ffmpeg列出AVFoundation音频设备
	cmd := getFFmpegCmd("-f", "avfoundation", "-list_devices", "true", "-i", "")
	output, _ := cmd.CombinedOutput()
	outputStr := string(output)

	// 检查设备是否在列表中
	if !strings.Contains(outputStr, deviceID) {
		return fmt.Errorf("音频设备未找到: %s", deviceID)
	}

	// 尝试打开设备
	testCmd := getFFmpegCmd(
		"-f", "avfoundation",
		"-i", fmt.Sprintf(":%s", deviceID), // macOS音频设备需要加冒号前缀
		"-t", "0.1",
		"-f", "null",
		"-y",
		"-hide_banner",
		"-loglevel", "error",
		"-",
	)
	if err := testCmd.Run(); err != nil {
		return fmt.Errorf("音频设备不可用: %v", err)
	}

	return nil
}

func NewStreamManager(globalWSManager *WebSocketManager) *StreamManager {
	return &StreamManager{
		streamStates:    make(map[string]*VideoStreamState),
		globalWSManager: globalWSManager,
	}
}

// ProcessVideoFrame 处理WebM流数据
func (sm *StreamManager) ProcessFrame(deviceID string, data []byte, isInit bool, isVideo bool) error {
	sm.mu.Lock()
	state, exists := sm.streamStates[deviceID]
	if !exists {
		state = &VideoStreamState{
			LastKeyFrame:  time.Now(),
			FrameCount:    0,
			KeyFrameCount: 0,
			LastTimestamp: time.Now().UnixNano() / 1e6,
		}
		sm.streamStates[deviceID] = state
	}
	sm.mu.Unlock()

	// 检查是否包含Cluster头
	isCluster := false
	if len(data) >= 4 {
		isCluster = bytes.Equal(data[:4], []byte{0x1F, 0x43, 0xB6, 0x75})
	}

	// 构建帧头
	// frameType: 0x10 = 初始化段, 0x20 = 关键帧, 0x30 = 普通帧
	var frameType byte
	if isVideo {
		if isInit {
			frameType = 0x10 // 初始化段
		} else if isCluster {
			frameType = 0x20 // 关键帧
		} else {
			frameType = 0x30 // 普通帧
		}
	} else {
		if isInit {
			frameType = 0x40 // 初始化段
		} else {
			frameType = 0x50 // 普通帧
		}
	}

	// 更新流状态
	state.FrameCount++
	if isInit || isCluster {
		state.KeyFrameCount++
		state.LastKeyFrame = time.Now()
	}

	// 时间戳使用毫秒
	currentTime := time.Now().UnixNano() / 1e6
	timestamp := uint32(currentTime)
	state.LastTimestamp = currentTime

	// 从deviceID生成设备索引（1-15）
	deviceIndex := byte(1) // 默认设备索引
	var sum int
	if len(deviceID) > 0 {
		// 使用简单的字符串求和哈希，更稳定且前后端更容易保持一致
		sum = 0
		for _, c := range deviceID {
			sum += int(c)
		}
		deviceIndex = byte((sum % 15) + 1) // 1-15，保留0用于特殊用途
	}

	// 构建帧头：4字节
	header := make([]byte, 4)
	header[0] = frameType | (deviceIndex & 0x0F) // 高4位frameType，低4位设备索引
	header[1] = byte(timestamp & 0x0F)           // 时间戳低4位
	header[2] = byte((timestamp >> 4) & 0xFF)    // 时间戳中间字节
	header[3] = byte((timestamp >> 12) & 0xFF)   // 时间戳高字节

	// 合并帧头和数据
	frame := append(header, data...)

	return sm.globalWSManager.SendFrame(deviceID, frame)
}

// 处理开始录制消息
func handleStartRecording(config struct {
	Configs []RecordingConfig `json:"configs"`
}) error {
	log.Printf("开始处理录制请求，配置: %+v", config)

	if len(config.Configs) == 0 {
		log.Printf("错误：录制配置为空")
		return fmt.Errorf("录制配置为空")
	}

	// 检查设备配置
	for i, cfg := range config.Configs {
		// 验证视频设备
		if err := validateDevice(cfg.VideoDevice, "video", i); err != nil {
			return err
		}

		// 验证音频设备
		if err := validateDevice(cfg.AudioDevice, "audio", i); err != nil {
			return err
		}
	}

	// 创建新的录制会话
	session := newRecordingSession("recording", config.Configs)
	log.Printf("创建新会话: %s, %+v", session.ID, config.Configs)

	// 启动录制
	if err := startMultiStreamRecording(session); err != nil {
		log.Printf("启动录制失败: %v", err)
		return fmt.Errorf("启动录制失败: %v", err)
	}

	// 保存会话信息
	sessionMutex.Lock()
	activeSessions[session.ID] = session
	sessionMutex.Unlock()

	log.Printf("录制会话已启动: %s", session.ID)

	// 发送成功响应
	response := map[string]interface{}{
		"type":    "recording_started",
		"session": session,
		"status":  "success",
		"data": map[string]interface{}{
			"sessionId": session.ID,
			"startTime": session.StartTime,
			"devices":   session.Configs,
		},
	}

	// 使用WebSocket管理器发送响应
	if err := globalWSManager.SendJSON(response); err != nil {
		log.Printf("发送响应失败: %v", err)
		return fmt.Errorf("发送响应失败: %v", err)
	}

	return nil
}

// 验证设备配置
func validateDevice(device *Device, deviceType string, configIndex int) error {
	if device == nil {
		return fmt.Errorf("配置 %d: %s设备为空", configIndex, deviceType)
	}

	if device.ID == "" {
		return fmt.Errorf("配置 %d: %s设备ID为空", configIndex, deviceType)
	}

	// 检查设备可用性
	if err := testDeviceAccess(device); err != nil {
		return fmt.Errorf("%s设备访问失败: %v", deviceType, err)
	}

	// 对视频设备进行额外的格式验证
	if deviceType == "video" {
		if device.CurrentFormat == nil {
			return fmt.Errorf("配置 %d: 视频设备当前格式为空", configIndex)
		}

		if device.CurrentFormat.Resolution.Width == 0 || device.CurrentFormat.Resolution.Height == 0 {
			return fmt.Errorf("配置 %d: 无效的分辨率: %+v", configIndex, device.CurrentFormat.Resolution)
		}

		// 验证帧率
		if device.CurrentFormat.FrameRate.Num == 0 || device.CurrentFormat.FrameRate.Den == 0 {
			// 设置默认帧率为30fps
			device.CurrentFormat.FrameRate = FrameRate{Num: 30, Den: 1}
			log.Printf("配置 %d: 使用默认帧率 30fps", configIndex)
		}

		// 验证像素格式
		if device.CurrentFormat.PixelFormat == "" {
			device.CurrentFormat.PixelFormat = "yuv420p"
			log.Printf("配置 %d: 使用默认像素格式 yuv420p", configIndex)
		}
	}

	return nil
}

// 处理停止录制消息
func handleStopRecording(sessionID string, conn *websocket.Conn) error {
	log.Printf("处理停止录制请求，会话ID: %s", sessionID)

	// 获取会话信息
	sessionMutex.Lock()
	session, exists := activeSessions[sessionID]
	if !exists {
		sessionMutex.Unlock()
		log.Printf("未找到录制会话: %s，当前活动会话: %v", sessionID, activeSessions)
		return fmt.Errorf("未找到录制会话: %s", sessionID)
	}

	log.Printf("找到录制会话，状态: %s", session.Status)

	// 停止会话
	session.Stop()

	// 等待所有FFmpeg进程完全退出和文件写入完成
	time.Sleep(2 * time.Second)

	// 从活动会话中移除
	delete(activeSessions, sessionID)
	sessionMutex.Unlock()

	// 发送成功响应
	response := map[string]interface{}{
		"type":    "recording_stopped",
		"session": session,
	}

	log.Printf("发送停止录制响应: %+v", response)
	if err := conn.WriteJSON(response); err != nil {
		log.Printf("发送停止录制响应失败: %v", err)
	}

	// 发送最终状态更新
	cleanupResponse := map[string]interface{}{
		"type":    "cleanup_completed",
		"session": session,
	}

	log.Printf("发送清理完成状态: %+v", cleanupResponse)
	if err := conn.WriteJSON(cleanupResponse); err != nil {
		log.Printf("发送清理完成状态失败: %v", err)
	}

	handleVideoMerging(session)

	return nil
}

// 获取ffmpeg命令
func getFFmpegCmd(args ...string) *exec.Cmd {
	return exec.Command(ffmpegPath, args...)
}

func startPreview(session *RecordingSession) error {
	if session == nil {
		return fmt.Errorf("session is nil")
	}
	if len(session.Configs) == 0 {
		return fmt.Errorf("no recording configs provided")
	}
	// 确保通道已初始化
	session.doneMutex.Lock()
	if session.done == nil {
		session.done = make(chan struct{})
	}
	session.doneMutex.Unlock()

	session.streamMutex.Lock()
	if session.streamDone == nil {
		session.streamDone = make(chan struct{})
	}
	session.streamMutex.Unlock()

	// 创建一个等待组来跟踪所有goroutine
	var wg sync.WaitGroup

	for _, config := range session.Configs {
		// 确保设备路径已设置
		if config.VideoDevice != nil {
			devicePath := getDevicePath(config.VideoDevice)
			if devicePath == "" {
				return fmt.Errorf("无法获取视频设备路径: %s", config.VideoDevice.Type)
			}
			config.VideoDevice.Path = devicePath
			log.Printf("视频设备路径: %s", devicePath)
			if _, err := startVideoCapture(config.VideoDevice, false); err != nil {
				return fmt.Errorf("启动视频捕获失败: %v", err)
			}
		}

		if config.AudioDevice != nil {
			devicePath := getDevicePath(config.AudioDevice)
			if devicePath == "" {
				return fmt.Errorf("无法获取音频设备路径: %s", config.AudioDevice.Type)
			}
			config.AudioDevice.Path = devicePath
			log.Printf("音频设备路径: %s", devicePath)
			if _, err := startAudioCapture(config.AudioDevice, false); err != nil {
				return fmt.Errorf("启动音频捕获失败: %v", err)
			}
		}

		// 增加等待组计数
		wg.Add(1)
		go func(config RecordingConfig) {
			defer wg.Done()

			defer func() {
				if r := recover(); r != nil {
					log.Printf("Recording goroutine panic: %v", r)
					stack := make([]byte, 4096)
					stack = stack[:runtime.Stack(stack, false)]
					log.Printf("Stack trace: %s", stack)
				}
			}()

			// 等待会话结束
			<-session.done

			// 停止视频捕获
			if config.VideoDevice != nil && config.VideoDevice.cmd != nil {
				if runtime.GOOS == "windows" {
					pid := config.VideoDevice.cmd.Process.Pid

					// 检查进程是否还在运行
					checkCmd := exec.Command("tasklist", "/FI", fmt.Sprintf("PID eq %d", pid), "/NH")
					output, _ := checkCmd.Output()
					processExists := len(output) > 0 && strings.Contains(string(output), strconv.Itoa(pid))

					if processExists {
						// 进程还在运行，尝试终止
						killCmd := exec.Command("taskkill", "/T", "/PID", strconv.Itoa(pid))
						if err := killCmd.Run(); err != nil {
							log.Printf("尝试正常终止视频进程失败: %v，将使用强制终止", err)
							// 如果正常终止失败，使用强制终止
							killCmd = exec.Command("taskkill", "/F", "/T", "/PID", strconv.Itoa(pid))
							if err := killCmd.Run(); err != nil {
								log.Printf("强制终止视频进程失败: %v", err)
							} else {
								log.Printf("已强制终止视频进程: %d", pid)
							}
						} else {
							log.Printf("已正常终止视频进程: %d", pid)
						}

						// 等待进程退出，设置超时
						done := make(chan error, 1)
						go func() {
							_, err := config.VideoDevice.cmd.Process.Wait()
							done <- err
						}()

						select {
						case err := <-done:
							if err != nil && !strings.Contains(err.Error(), "wait") {
								log.Printf("等待视频进程时发生错误: %v", err)
							}
						case <-time.After(2 * time.Second):
							log.Printf("等待视频进程退出超时")
						}
					}
				} else {
					if err := config.VideoDevice.cmd.Process.Signal(syscall.SIGINT); err != nil {
						log.Printf("停止视频捕获失败: %v", err)
					}
					config.VideoDevice.cmd.Wait()
				}
			}

			// 停止音频捕获
			if config.AudioDevice != nil && config.AudioDevice.cmd != nil {
				if runtime.GOOS == "windows" {
					pid := config.AudioDevice.cmd.Process.Pid

					// 检查进程是否还在运行
					checkCmd := exec.Command("tasklist", "/FI", fmt.Sprintf("PID eq %d", pid), "/NH")
					output, _ := checkCmd.Output()
					processExists := len(output) > 0 && strings.Contains(string(output), strconv.Itoa(pid))

					if processExists {
						// 进程还在运行，尝试终止
						killCmd := exec.Command("taskkill", "/T", "/PID", strconv.Itoa(pid))
						if err := killCmd.Run(); err != nil {
							log.Printf("尝试正常终止音频进程失败: %v，将使用强制终止", err)
							// 如果正常终止失败，使用强制终止
							killCmd = exec.Command("taskkill", "/F", "/T", "/PID", strconv.Itoa(pid))
							if err := killCmd.Run(); err != nil {
								log.Printf("强制终止音频进程失败: %v", err)
							} else {
								log.Printf("已强制终止音频进程: %d", pid)
							}
						} else {
							log.Printf("已正常终止音频进程: %d", pid)
						}

						// 等待进程退出，设置较短的超时
						done := make(chan error, 1)
						go func() {
							_, err := config.AudioDevice.cmd.Process.Wait()
							done <- err
						}()

						select {
						case err := <-done:
							if err != nil && !strings.Contains(err.Error(), "wait") {
								log.Printf("等待音频进程时发生错误: %v", err)
							}
						case <-time.After(2 * time.Second):
							log.Printf("等待音频进程退出超时")
						}
					}
				} else {
					if err := config.AudioDevice.cmd.Process.Signal(syscall.SIGINT); err != nil {
						log.Printf("停止音频捕获失败: %v", err)
					}
					config.AudioDevice.cmd.Wait()
				}
			}
		}(config)
	}

	session.Status = "previewing"
	return nil
}

// 处理开始预览请求
func handleStartPreview(config RecordingConfig) error {
	if config.VideoDevice != nil {
		if err := validateDevice(config.VideoDevice, "video", 0); err != nil {
			return err
		}
	} else if config.AudioDevice != nil {
		if err := validateDevice(config.AudioDevice, "audio", 0); err != nil {
			return err
		}
	}

	// 创建新的录制会话
	session := newRecordingSession("preview", []RecordingConfig{
		{
			VideoDevice: config.VideoDevice,
			AudioDevice: config.AudioDevice,
		},
	})
	log.Printf("创建新会话: %s", session.ID)

	// 启动预览
	if err := startPreview(session); err != nil {
		log.Printf("启动预览失败: %v", err)
		return fmt.Errorf("启动预览失败: %v", err)
	}

	// 保存会话信息
	sessionMutex.Lock()
	activeSessions[session.ID] = session
	sessionMutex.Unlock()

	log.Printf("预览会话已启动: %s", session.ID)

	// 发送成功响应
	response := map[string]interface{}{
		"type":    "preview_started",
		"session": session,
		"status":  "success",
		"data": map[string]interface{}{
			"sessionId": session.ID,
			"startTime": session.StartTime,
			"devices":   session.Configs,
		},
	}

	// 使用WebSocket管理器发送响应
	if err := globalWSManager.SendJSON(response); err != nil {
		log.Printf("发送响应失败: %v", err)
		return fmt.Errorf("发送响应失败: %v", err)
	}

	return nil
}

// 处理停止预览请求
func handleStopPreview(sessionID string, conn *websocket.Conn) error {
	log.Printf("处理停止预览请求，会话ID: %s", sessionID)

	// 获取会话信息
	sessionMutex.Lock()
	session, exists := activeSessions[sessionID]
	if !exists {
		sessionMutex.Unlock()
		log.Printf("未找到预览会话: %s", sessionID)
		return fmt.Errorf("未找到预览会话: %s", sessionID)
	}

	// 检查会话类型
	if session.Type != "preview" {
		sessionMutex.Unlock()
		log.Printf("会话类型错误，期望preview，实际为: %s", session.Type)
		return fmt.Errorf("会话类型错误: %s", session.Type)
	}

	log.Printf("找到预览会话，状态: %s", session.Status)

	// 停止会话
	session.Stop()
	delete(activeSessions, sessionID)
	sessionMutex.Unlock()

	// 发送成功响应
	response := map[string]interface{}{
		"type":    "preview_stopped",
		"session": session,
	}

	log.Printf("发送停止预览响应: %+v", response)
	if err := conn.WriteJSON(response); err != nil {
		log.Printf("发送停止预览响应失败: %v", err)
	}

	return nil
}

func handleGetSessionID(conn *websocket.Conn) error {
	return conn.WriteJSON(map[string]interface{}{
		"type": "get_session_id",
		"session_id": func() string {
			for k := range activeSessions {
				return k
			}
			return "0"
		}(),
	})
}

// 处理视频流输出
func handleVideoStream(device *Device, stdout io.Reader) {
	defer device.cmd.Wait()

	// 使用更大的缓冲区来确保完整读取初始化段
	initBuffer := make([]byte, 1024*1024) // 使用1MB的缓冲区
	totalRead := 0
	isInitSent := false

	// 循环读取直到找到完整的初始化段
	for !isInitSent {
		n, err := stdout.Read(initBuffer[totalRead:])
		if err != nil {
			if err != io.EOF {
				log.Printf("读取初始化段失败: %v", err)
				return
			}
			break
		}
		totalRead += n

		// 确保至少有足够的数据进行解析
		if totalRead < 8 {
			continue
		}

		// 查找EBML头
		ebmlPos := bytes.Index(initBuffer[:totalRead], []byte{0x1A, 0x45, 0xDF, 0xA3})
		if ebmlPos == -1 {
			if totalRead >= len(initBuffer) {
				log.Printf("缓冲区已满但未找到EBML头")
				return
			}
			continue
		}

		// 查找Segment头
		segmentPos := bytes.Index(initBuffer[ebmlPos:totalRead], []byte{0x18, 0x53, 0x80, 0x67})
		if segmentPos == -1 {
			if totalRead >= len(initBuffer) {
				log.Printf("缓冲区已满但未找到Segment头")
				return
			}
			continue
		}
		segmentPos += ebmlPos

		// 查找第一个Cluster头
		clusterPos := bytes.Index(initBuffer[segmentPos:totalRead], []byte{0x1F, 0x43, 0xB6, 0x75})
		if clusterPos == -1 {
			if totalRead >= len(initBuffer) {
				log.Printf("缓冲区已满但未找到Cluster头")
				return
			}
			continue
		}
		clusterPos += segmentPos

		// 查找Info元素
		infoPos := bytes.Index(initBuffer[segmentPos:clusterPos], []byte{0x15, 0x49, 0xA9, 0x66})
		if infoPos == -1 {
			log.Printf("未找到Info元素")
			return
		}
		infoPos += segmentPos

		// 发送初始化段（包括EBML头、Segment头和Info元素）
		if err := sendFrame(device.ID, initBuffer[:infoPos+4], true, true); err != nil {
			log.Printf("发送初始化段失败: %v", err)
			return
		}
		log.Printf("成功发送初始化段，长度: %d", infoPos+4)

		// 等待一小段时间确保初始化段被处理
		time.Sleep(100 * time.Millisecond)

		// 发送第一个Cluster数据
		if err := sendFrame(device.ID, initBuffer[infoPos+4:totalRead], false, true); err != nil {
			log.Printf("发送第一个Cluster失败: %v", err)
			return
		}
		log.Printf("成功发送第一个Cluster，长度: %d", totalRead-(infoPos+4))

		isInitSent = true
	}

	// 继续读取和发送后续数据
	buffer := make([]byte, 512*1024)
	for {
		n, err := stdout.Read(buffer)
		if err != nil {
			return
		}
		if n > 0 {
			if err := sendFrame(device.ID, buffer[:n], false, true); err != nil {
				log.Printf("发送WebM帧失败: %v", err)
				return
			}
		}
	}
}

func handleVideoMerging(session *RecordingSession) {
	// 等待所有录制进程完成
	time.Sleep(1 * time.Second)

	// 遍历所有配置进行音视频合并
	for i := range session.Configs {
		videoPath := session.TempFiles[fmt.Sprintf("video_%d", i)]
		audioPath := session.TempFiles[fmt.Sprintf("audio_%d", i)]

		if videoPath == "" || audioPath == "" {
			log.Printf("找不到临时文件路径: video=%s, audio=%s", videoPath, audioPath)
			continue
		}

		// 等待文件写入完成
		time.Sleep(1 * time.Second)

		outputPath := filepath.Join("recordings", fmt.Sprintf("stream_%d_%s.mp4", i, session.StartTime.Format("20060102_150405.000000")))

		// 合并音视频文件
		if err := convertToMP4WithResolution(
			videoPath,
			outputPath,
			audioPath,
			&session.Configs[i].VideoDevice.CurrentFormat.Resolution,
		); err != nil {
			log.Printf("合并音视频失败: %v", err)
			continue
		}
		log.Printf("转换MP4完成: %s", outputPath)

		// 清理临时文件
		//os.Remove(videoPath)
		//os.Remove(audioPath)
		delete(session.TempFiles, fmt.Sprintf("video_%d", i))
		delete(session.TempFiles, fmt.Sprintf("audio_%d", i))
	}
}
