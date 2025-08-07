package main

import (
	"fmt"
	"io"
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
	Cmd    []string
	Keymap map[string]string
	Hold   bool
	Input  string
}

func parseFlag() ParsedFlag {
	parsed := ParsedFlag{
		Keymap: make(map[string]string),
	}
	printHelp := func() {
		log.Fatal("Usage: keywrap --bind \"ctrl-e:become(nvim a.json)\" -- bat a.json")
	}

	args := os.Args[1:]
	for len(args) > 0 {
		switch args[0] {
		case "--":
			parsed.Cmd = args[1:]
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
			parsed.Cmd = args
			args = nil
		}
	}
	if len(parsed.Cmd) == 0 {
		printHelp()
	}
	return parsed
}

func collectStdinToFile() *os.File {
	if term.IsTerminal(int(os.Stdin.Fd())) {
		return nil
	}
	stdinFile, err := os.CreateTemp("", "keywrap-stdin")
	if err != nil {
		panic(err)
	}
	io.Copy(stdinFile, os.Stdin)

	return stdinFile
}

func startPty(cmd []string, preInput string) (*exec.Cmd, *os.File) {
	child := exec.Command(cmd[0], cmd[1:]...)
	child.Env = os.Environ()

	ptmx, err := pty.Start(child)
	if err != nil {
		panic(err)
	}

	if preInput != "" {
		_, err = ptmx.Write([]byte(preInput))
		if err != nil {
			panic(err)
		}
	}

	return child, ptmx
}

func main() {
	log.SetFlags(0)

	flag := parseFlag()
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		panic(err)
	}

	childCmd := flag.Cmd

	stdinFile := collectStdinToFile()
	if stdinFile != nil {
		defer stdinFile.Close()
		childCmd = append([]string{"bash", "-c", `"$@" <"$0"; rm "$0"`, stdinFile.Name()}, childCmd...)
	}

	child, ptmx := startPty(childCmd, flag.Input)
	defer ptmx.Close()

	// 设置终端为原始模式，以便直接读取按键
	oldState, err := term.MakeRaw(int(tty.Fd()))
	if err != nil {
		panic(err)
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
				return
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
				return
			case ActionTypeBecome:
				stopChild()
				arg := strings.ReplaceAll(action.Arg, "__stdin_file__", stdinFile.Name())
				execSyscall("bash", "-c", arg)
			case ActionTypeExecute:
				arg := strings.ReplaceAll(action.Arg, "__stdin_file__", stdinFile.Name())
				cmd := exec.Command("bash", "-c", arg)
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
		panic(lookErr)
	}
	env := os.Environ()
	execErr := syscall.Exec(binary, append([]string{binary}, args...), env)
	if execErr != nil {
		panic(execErr)
	}
}
