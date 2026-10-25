package main

import (
	"fmt"
	"net"
	"time"

	"golang.org/x/crypto/ssh"
)

func (a *App) sshRun(cmd string) (string, error) {
	if a.cfg.Mock {
		return "mock mode: command not executed\n", nil
	}
	clientCfg := &ssh.ClientConfig{
		User:              a.cfg.RouterUser,
		HostKeyCallback:   ssh.InsecureIgnoreHostKey(),
		Timeout:           8 * time.Second,
		HostKeyAlgorithms: []string{"ssh-rsa", "rsa-sha2-256", "rsa-sha2-512", "ssh-ed25519", "ecdsa-sha2-nistp256"},
	}
	if a.cfg.RouterKey != "" {
		signer, err := ssh.ParsePrivateKey([]byte(a.cfg.RouterKey))
		if err != nil {
			return "", fmt.Errorf("parse private key: %w", err)
		}
		clientCfg.Auth = append(clientCfg.Auth, ssh.PublicKeys(signer))
	}
	if a.cfg.RouterPassword != "" {
		clientCfg.Auth = append(clientCfg.Auth, ssh.Password(a.cfg.RouterPassword))
	}
	if len(clientCfg.Auth) == 0 {
		return "", fmt.Errorf("router credentials not configured")
	}

	client, err := ssh.Dial("tcp", net.JoinHostPort(a.cfg.RouterHost, "22"), clientCfg)
	if err != nil {
		return "", fmt.Errorf("ssh dial %s: %w", a.cfg.RouterHost, err)
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

func (a *App) sshOK() bool {
	if a.cfg.Mock {
		return true
	}
	_, err := a.sshRun("true")
	return err == nil
}
