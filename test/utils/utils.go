package utils

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"

	. "github.com/onsi/ginkgo/v2"
)

// Kubectl execute kubectl cli
func Kubectl(args ...string) {
	GinkgoHelper()

	cmd := exec.Command("kubectl", args...)

	var stderr bytes.Buffer

	cmd.Stderr = &stderr

	err := cmd.Start()
	if err != nil {
		Fail(fmt.Sprintf("Error: %v", err))
	}

	err = cmd.Wait()
	if err != nil {
		Fail(fmt.Sprintf("`kubectl %s` failed: %s", strings.Join(args, " "), stderr.String()))
	}
}

func KubectlWithOutput(args ...string) (string, error) {
	kubectlCmd := exec.Command("kubectl", args...)

	output, err := kubectlCmd.CombinedOutput()
	if err != nil {
		// Reformat error to include kubectl command and stderr output
		err = fmt.Errorf(
			"error running command '%s':\n %s: %s",
			strings.Join(kubectlCmd.Args, " "),
			output,
			err.Error(),
		)
	}

	return string(output), err
}

// FindLogMessage opens the file "e2e.log" and searches for the given message string.
// It returns true if the message is found in any line of the file, otherwise false.
func FindLogMessage(msg string) bool {
	file, err := os.Open("../e2e/e2e.log")
	if err != nil {
		// Handle the error if the file cannot be opened.
		// For an e2e log, it might mean the test hasn't run yet or the path is wrong.
		fmt.Printf("Error opening e2e.log: %v\n", err)
		return false
	}
	defer func() {
		if cerr := file.Close(); cerr != nil {
			fmt.Printf("Warning: error closing file 'e2e.log': %v\n", cerr)
		}
	}()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.Contains(line, msg) {
			return true // Message found
		}
	}

	if err := scanner.Err(); err != nil {
		// Handle potential errors during scanning (e.g., I/O errors)
		fmt.Printf("Error reading e2e.log: %v\n", err)
	}

	return false // Message not found after scanning the entire file
}

// EmptyLogFile truncates the specified file to zero length.
// If the file does not exist, it creates an empty file.
func EmptyLogFile() error {
	// Open the file in write-only mode, create if it doesn't exist, and truncate its content.
	file, err := os.OpenFile("../e2e/e2e.log", os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("failed to open or create file %s for emptying: %w", "e2e.log", err)
	}
	defer func() {
		if cerr := file.Close(); cerr != nil {
			fmt.Printf("Warning: error closing file 'e2e.log': %v\n", cerr)
		}
	}()

	fmt.Printf("File '%s' has been emptied.\n", "e2e.log")
	return nil
}
