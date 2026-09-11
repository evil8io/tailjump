package sshc

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

// agentAuthMethod connects to the ssh-agent at $SSH_AUTH_SOCK and returns an
// AuthMethod backed by its keys. The caller closes the returned connection
// once the handshake using it is done.
func agentAuthMethod() (ssh.AuthMethod, io.Closer, error) {
	sock := os.Getenv("SSH_AUTH_SOCK")
	if sock == "" {
		return nil, nil, errors.New("SSH_AUTH_SOCK is not set")
	}
	conn, err := net.Dial("unix", sock)
	if err != nil {
		return nil, nil, fmt.Errorf("dial ssh-agent: %w", err)
	}
	client := agent.NewClient(conn)
	return ssh.PublicKeysCallback(client.Signers), conn, nil
}

// keyboardInteractiveMethod prints every prompt to stderr, so a check-mode
// sign-in URL reaches the user on whichever channel carries it, and answers
// each question with an empty string.
func keyboardInteractiveMethod() ssh.AuthMethod {
	return ssh.KeyboardInteractive(func(name, instruction string, questions []string, _ []bool) ([]string, error) {
		if name != "" {
			fmt.Fprintln(os.Stderr, name)
		}
		if instruction != "" {
			fmt.Fprintln(os.Stderr, instruction)
		}
		answers := make([]string, len(questions))
		for i, q := range questions {
			fmt.Fprintln(os.Stderr, q)
			answers[i] = ""
		}
		return answers, nil
	})
}
