package utils

import (
	"bytes"
	"fmt"
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
