package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"golang.org/x/exp/rand"
)

// rbench is a cli tool to benchmark golang packages on remote servers using AWS cloud.
//
// usage is similar to go test -bench=. ... ;
// under the hood, rbench will cross compile the package (go test -c) and upload the binary on the remote machine.
// then it launches the benchmark (and forward the options) and stream the output to the local machine.
// once the benchmark ssh session is closed, it will terminate the remote machine.
//
// the ec2 instance is launch as needed using aws sdk; the instance type is configurable.

// define the flags
var (
	// same as go test ...
	benchFlag = flag.String("bench", ".", "run only those benchmarks matching a regular expression")
	countFlag = flag.Int("count", 5, "run each benchmark n times")
	cpuFlag   = flag.Int("cpu", 0, "number of parallel CPUs to use")
	benchMem  = flag.Bool("benchmem", false, "print memory allocation statistics")
	run       = flag.String("run", "NONE", "run only those tests and examples matching the regular expression")

	// instance type
	instanceType = flag.String("type", "t2.micro", "ec2 instance type")
)

const clearStr = "                                                                                                            "

func main() {
	// first we cross build the package for amd64 target
	// then we spin up an ec2 instance
	// then we upload the binary to the instance
	// then we run the benchmark
	// then we stream the output to the local machine
	// then we terminate the instance

	// parse the flags
	flag.Parse()

	commitID, err := gitCommitID()
	if err != nil {
		fmt.Printf("error: %v\n", err)
		return
	}

	benchFileName, err := compileBenchmarkBinary()
	if err != nil {
		fmt.Printf("error: %v\n", err)
		return
	}

	// init aws sdk objects
	err = initAWS()
	if err != nil {
		fmt.Printf("error: %v\n", err)
		return
	}

	// create a new ec2 instance
	fmt.Printf("\rstarting %s instance..."+clearStr, *instanceType)
	publicIP, instanceID, err := startInstance()
	if err != nil {
		fmt.Printf("error: %v\n", err)
		return
	}

	// Create a channel to listen for incoming signals
	sigChan := make(chan os.Signal, 1)
	// Notify the channel for interrupt (Ctrl+C) and termination signals
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGQUIT)

	go func() {
		// print status
		fmt.Printf("\rssh ready (%s). uploading benchmark binary..."+clearStr, publicIP)

		// upload the binary
		err = scp(benchFileName, publicIP)
		if err != nil {
			fmt.Printf("error: %v\n", err)
			close(sigChan)
			return
		}

		fmt.Printf("\rrunning benchmark..." + clearStr + "\n")
		// write header
		fmt.Printf("ec2-user: %s\n", awsUserName)
		fmt.Printf("instance IP: %s\n", publicIP)
		fmt.Printf("instance type: %s\n", *instanceType)
		fmt.Printf("commit ID: %s\n", commitID)

		// execute the benchmark
		err = sshExec(publicIP)
		if err != nil {
			fmt.Printf("error: %v\n", err)
		}
		close(sigChan)
	}()

	// Wait for a signal
	<-sigChan
	terminateInstance(instanceID)

	// Exit the program gracefully
	os.Exit(0)

}

func sshExec(publicIP string) error {
	args := []string{"-i", privateKeyPath(),
		fmt.Sprintf("ubuntu@%s", publicIP),
		"cd /tmp && ./bench",
		fmt.Sprintf("-test.bench=%s", *benchFlag),
		fmt.Sprintf("-test.count=%d", *countFlag),
		fmt.Sprintf("-test.benchmem=%t", *benchMem),
		fmt.Sprintf("-test.run=%s", *run),
	}
	if *cpuFlag > 0 {
		args = append(args, fmt.Sprintf("-test.cpu=%d", *cpuFlag))
	}

	cmd := exec.Command("ssh", args...)

	// Stream stdout and stderr
	stdoutPipe, _ := cmd.StdoutPipe()
	stderrPipe, _ := cmd.StderrPipe()

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start the SSH command: %v", err)
	}

	// Goroutines to handle real-time streaming
	go io.Copy(os.Stdout, stdoutPipe)
	go io.Copy(os.Stderr, stderrPipe)

	// Wait for the command to complete
	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("failed to run the benchmark: %v", err)
	}

	return nil
}

func scp(benchFileName, publicIP string) error {
	cmd := exec.Command("scp", "-o", "StrictHostKeyChecking=no", "-i", privateKeyPath(), benchFileName, fmt.Sprintf("ubuntu@%s:/tmp/bench", publicIP))
	var uploadStdout, uploadStderr strings.Builder
	cmd.Stdout = &uploadStdout
	cmd.Stderr = &uploadStderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to upload the binary: \nstdout: %s\nstderr: %s, %v", uploadStdout.String(), uploadStderr.String(), err)
	}
	return nil
}

func compileBenchmarkBinary() (fileName string, err error) {
	// cross build the package
	// GOOS=linux GOARCH=amd64 go test -c -o /tmp/bench
	benchFileName := "/tmp/bench-" + randString(7)

	cmd := exec.Command("go", "test", "-c", "-o", benchFileName)
	cmd.Env = append(os.Environ(), "GOOS=linux", "GOARCH=amd64")
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("failed to cross build the package: \nstdout: %s\nstderr: %s, %v", stdout.String(), stderr.String(), err)
	}

	// check that the binary has been created
	if _, err := os.Stat(benchFileName); err != nil {
		return "", fmt.Errorf("binary not found: %v - cmd %s failed", err, cmd.String())
	}

	return benchFileName, nil
}

func gitCommitID() (string, error) {
	// Check if the directory is a Git repository
	cmd := exec.Command("git", "rev-parse", "--is-inside-work-tree")
	_, err := cmd.Output()
	if err != nil {
		return "not a git repo", nil
	}

	// Get the commit ID
	cmd = exec.Command("git", "rev-parse", "HEAD")
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	commitID := strings.TrimSpace(string(out))

	// Check if the working directory is dirty
	cmd = exec.Command("git", "status", "--porcelain")
	out, err = cmd.Output()
	if err != nil {
		return "", err
	}
	if len(out) > 0 {
		commitID += "-dirty"
	}

	return commitID, nil
}

func randString(n int) string {
	rand.Seed(uint64(time.Now().UnixNano()))
	const letters = "abcdefghijklmnopqrstuvwxyz"
	b := make([]byte, n)
	for i := range b {
		b[i] = letters[rand.Intn(len(letters))]
	}
	return string(b)
}
