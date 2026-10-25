package sshx

import (
	"fmt"
	"net"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

type Client struct {
	Host     string
	User     string
	Password string
	Key      string
	Timeout  time.Duration
}

func New(host, user, password, key string) *Client {
	return &Client{
		Host:     host,
		User:     user,
		Password: password,
		Key:      key,
		Timeout:  8 * time.Second,
	}
}

func (c *Client) Run(cmd string) (string, error) {
	clientCfg := &ssh.ClientConfig{
		User:              c.User,
		HostKeyCallback:   ssh.InsecureIgnoreHostKey(),
		Timeout:           c.Timeout,
		HostKeyAlgorithms: []string{"ssh-rsa", "rsa-sha2-256", "rsa-sha2-512", "ssh-ed25519", "ecdsa-sha2-nistp256"},
	}
	if c.Key != "" {
		signer, err := ssh.ParsePrivateKey([]byte(c.Key))
		if err != nil {
			return "", fmt.Errorf("parse private key: %w", err)
		}
		clientCfg.Auth = append(clientCfg.Auth, ssh.PublicKeys(signer))
	}
	if c.Password != "" {
		clientCfg.Auth = append(clientCfg.Auth, ssh.Password(c.Password))
	}
	if len(clientCfg.Auth) == 0 {
		return "", fmt.Errorf("router credentials not configured")
	}

	client, err := ssh.Dial("tcp", net.JoinHostPort(c.Host, "22"), clientCfg)
	if err != nil {
		return "", fmt.Errorf("ssh dial %s: %w", c.Host, err)
	}
	defer client.Close()

	session, err := client.NewSession()
	if err != nil {
		return "", fmt.Errorf("ssh session: %w", err)
	}
	defer session.Close()

	out, err := session.CombinedOutput(cmd)
	return string(out), err
}

func (c *Client) OK() bool {
	_, err := c.Run("true")
	return err == nil
}

func Quote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
