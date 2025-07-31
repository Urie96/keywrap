package main

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/creack/pty"
	"golang.org/x/term"
)

type ParsedFlag struct {
	Cmd    string
	Args   []string
	Keymap map[string]string
	Hold   bool
	Input  string
}

func parseFlag() ParsedFlag {
	parsed := ParsedFlag{
		Keymap: make(map[string]string),
	}
	printHelp := func() {
		log.Println("Usage: keywrap --bind \"ctrl-e:become(nvim a.json)\" -- bat a.json")
		os.Exit(1)
	}

	args := os.Args[1:]
	for len(args) > 0 {
		switch args[0] {
		case "--":
			parsed.Cmd = args[1]
			parsed.Args = args[2:]
			args = nil
		case "--bind":
			keymap := strings.SplitN(args[1], ":", 2)
			if len(keymap) != 2 {
				printHelp()
			}
			parsed.Keymap[keymap[0]] = strings.TrimSpace(keymap[1])
			args = args[2:]
		case "--hold", "-h":
			parsed.Hold = true
			args = args[1:]
		case "--input":
			parsed.Input = args[1]
			args = args[2:]
		default:
			parsed.Cmd = args[0]
			parsed.Args = args[1:]
			args = nil
		}
	}
	if parsed.Cmd == "" {
		printHelp()
	}
	return parsed
}

func mainWithReturn() int {
	log.SetFlags(0)

	flag := parseFlag()
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		log.Printf("Failed to open /dev/tty: %v", err)
		return 1
	}

	// 准备要执行的命令
	child := exec.Command(flag.Cmd, flag.Args...)
	child.Env = os.Environ()

	// 创建伪终端
	ptmx, err := pty.Start(child)
	if err != nil {
		log.Printf("Failed to start command: %v\n", err)
		return 1
	}
	defer ptmx.Close()

	if flag.Input != "" {
		_, err = ptmx.Write([]byte(flag.Input))
		if err != nil {
			log.Printf("Failed to write input: %v\n", err)
			return 1
		}
	}

	// 设置终端为原始模式，以便直接读取按键
	oldState, err := term.MakeRaw(int(tty.Fd()))
	if err != nil {
		log.Printf("Failed to set terminal to raw mode: %v\n", err)
		return 1
	}
	defer term.Restore(int(tty.Fd()), oldState)

	// 处理终端大小变化
	sigWinchChan := make(chan os.Signal, 1)
	signal.Notify(sigWinchChan, syscall.SIGWINCH)
	sigWinchChan <- syscall.SIGWINCH // 初始调整大小

	childExitChan := make(chan error, 1)
	go func() {
		defer close(childExitChan)
		childExitChan <- child.Wait()
	}()

	actionChan := make(chan Action, 10)

	go func() {
		buf := make([]byte, 1024)
		keymap := formatKeymap(flag.Keymap)
		isDebug := os.Getenv("DEBUG") == "1"
		for {
			n, err := tty.Read(buf)
			if err != nil {
				return
			}
			received := buf[:n]
			if isDebug {
				log.Printf("%q %v %s\n", received, received, keymap[string(received)])
			} else if action, ok := keymap[string(received)]; ok {
				actionChan <- action
			} else if childExitChan == nil {
				actionChan <- Action{
					Type: ActionTypeExit,
				}
			} else {
				// 转发其他按键
				_, err = ptmx.Write(received)
				if err != nil {
					return
				}
			}
		}
	}()

	// 将命令输出复制到标准输出
	go func() {
		buf := make([]byte, 1024)
		for {
			n, err := ptmx.Read(buf)
			if err != nil {
				return
			}
			os.Stdout.Write(buf[:n])
		}
	}()

	stopChild := func() {
		if childExitChan == nil {
			return
		}

		// 发送SIGTERM信号
		err := child.Process.Signal(syscall.SIGTERM)
		if err != nil {
			log.Printf("Error sending SIGTERM to child: %v\n", err)
		}

		for {
			select {
			case <-time.After(2 * time.Second):
				// 超时后强制杀死子进程
				log.Println("Child process did not exit gracefully, sending SIGKILL")
				err := child.Process.Kill()
				if err != nil {
					log.Printf("Error killing child process: %v\n", err)
				}
			case <-childExitChan:
				childExitChan = nil
				return
			}
		}
	}

	for {
		select {
		case err := <-childExitChan:
			childExitChan = nil
			if err != nil {
				log.Printf("Command finished with error: %v\n", err)
			}
			if !flag.Hold {
				return 0
			} else {
				log.Println("Child process exited, but --hold option is set, waiting for input...")
			}
		case <-sigWinchChan:
			if err := pty.InheritSize(tty, ptmx); err != nil {
				log.Printf("Error resizing pty: %v\n", err)
			}
		case action := <-actionChan:
			switch action.Type {
			case ActionTypeExit:
				stopChild()
				return 0
			case ActionTypeBecome:
				stopChild()
				execSyscall("bash", "-c", action.Arg)
			case ActionTypeExecute:
				cmd := exec.Command("bash", "-c", action.Arg)
				cmd.Stdout = os.Stdout
				cmd.Stderr = os.Stderr
				if err := cmd.Run(); err != nil {
					log.Println(err)
				}
			}
		}
	}
}

type Action struct {
	Type ActionType
	Arg  string
}

type ActionType string

const (
	ActionTypeExit    ActionType = "exit"
	ActionTypeBecome  ActionType = "become"
	ActionTypeExecute ActionType = "execute"
)

func formatKeymap(keymap map[string]string) map[string]Action {
	m := make(map[string]Action)
	for k, v := range keymap {
		var action Action
		if v == "exit" {
			action = Action{
				Type: ActionTypeExit,
			}
		} else if strings.HasPrefix(v, "become(") {
			action = Action{
				Type: ActionTypeBecome,
				Arg:  v[7 : len(v)-1],
			}
		} else if strings.HasPrefix(v, "execute(") {
			action = Action{
				Type: ActionTypeExecute,
				Arg:  v[8 : len(v)-1],
			}
		}

		switch {
		case len(k) == 1:
			m[k] = action
		case strings.HasPrefix(k, "ctrl-") && len(k[5:]) == 1:
			code := k[5]
			m[fmt.Sprintf("\x1b[%d;5u", code)] = action // CSI u
			if code >= 'a' && code <= 'z' {
				m[string(code-'a'+1)] = action
			}
		case k == "enter":
			m["\n"] = action
		case k == "tab":
			m["\t"] = action
		default:
			panic("unknown key: " + k)
		}
	}
	return m
}

func execSyscall(cmd string, args ...string) {
	binary, lookErr := exec.LookPath(cmd)
	if lookErr != nil {
		log.Fatal(lookErr)
	}
	env := os.Environ()
	execErr := syscall.Exec(binary, append([]string{binary}, args...), env)
	if execErr != nil {
		log.Fatal(execErr)
	}
}

func main() {
	code := mainWithReturn()
	if code != 0 {
		os.Exit(code)
	}
}
