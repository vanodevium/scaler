package main

import (
	"context"
	"fmt"
	"github.com/urfave/cli/v3"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"sync"
	"syscall"
	"time"
)

func runProcess(ctx context.Context, command string, pidChan chan<- int, wg *sync.WaitGroup) {
	backoff := time.Second
	maxBackoff := time.Second * 30

	for {
		select {
		case <-ctx.Done():
			return
		default:
			cmd := exec.Command(command)
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr

			if err := cmd.Start(); err != nil {
				fmt.Printf("Error starting process: %v\n", err)

				select {
				case <-ctx.Done():
					return
				case <-time.After(backoff):
					backoff = time.Duration(min(backoff.Seconds()*2, maxBackoff.Seconds())) * time.Second
					continue
				}
			}

			pidChan <- cmd.Process.Pid
			fmt.Printf("Started process with PID: %d\n", cmd.Process.Pid)

			done := make(chan error, 1)
			go func() {
				done <- cmd.Wait()
			}()

			backoff = time.Second

			select {
			case err := <-done:
				if err != nil {
					fmt.Printf("Process %d exited with error: %v\n", cmd.Process.Pid, err)
				}

				select {
				case <-ctx.Done():
					wg.Done()
					return
				default:
					continue
				}
			case <-ctx.Done():

				if cmd.Process != nil {
					fmt.Printf("Terminating process %d...\n", cmd.Process.Pid)
					if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
						_ = cmd.Process.Kill()
						return
					}

					select {
					case <-done:
					case <-time.After(5 * time.Second):
						fmt.Printf("Process %d did not terminate gracefully, forcing kill\n", cmd.Process.Pid)
						_ = cmd.Process.Kill()
					}
				}
				return
			}
		}
	}
}

func main() {
	cmd := &cli.Command{
		Name:                   "scale",
		Usage:                  "run process in the background with auto-restart",
		UseShortOptionHandling: true,
		Flags: []cli.Flag{
			&cli.IntFlag{
				Name:    "copies",
				Usage:   "how many copies to process",
				Aliases: []string{"c"},
				Value:   1,
			},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			command := cmd.Args().Get(0)
			if command == "" {
				return fmt.Errorf("command argument is required")
			}

			numCopies := int(cmd.Int("copies"))
			if numCopies < 1 {
				return fmt.Errorf("copies must be at least 1")
			}

			ctx, cancel := context.WithCancel(ctx)
			defer cancel()

			processes := make([]int, 0, numCopies)
			processesChannels := make(chan int, numCopies)
			var wg sync.WaitGroup

			fmt.Printf("Starting %d copies of '%s'...\n", numCopies, command)

			for i := 0; i < numCopies; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					runProcess(ctx, command, processesChannels, &wg)
				}()
			}

			for i := 0; i < numCopies; i++ {
				processes = append(processes, <-processesChannels)
			}

			sigChan := make(chan os.Signal, 1)
			signal.Notify(sigChan, syscall.SIGTERM, syscall.SIGINT)

			sig := <-sigChan
			fmt.Printf("\nReceived %v signal. Stopping all processes...\n", sig)

			cancel()

			fmt.Println("Waiting for all processes to terminate...")
			wg.Wait()

			fmt.Println("All processes are terminated")
			os.Exit(0)
			return nil
		},
	}

	if err := cmd.Run(context.Background(), os.Args); err != nil {
		log.Fatal(err)
	}
}
