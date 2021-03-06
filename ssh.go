package main

import (
	"bytes"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"os"
	"os/user"
	"path/filepath"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

//
// Connection Setup
//

func loadPrivateKey(filepath string) (ssh.Signer, error) {
	pemBytes, err := ioutil.ReadFile(filepath)
	if err != nil {
		log.Fatal(err)
		return nil, err
	}

	signer, err := ssh.ParsePrivateKey(pemBytes)
	if err != nil {
		log.Fatal(err)
		return nil, err
	}

	return signer, nil
}

func generateConfig(username string, keypath string) (*ssh.ClientConfig, error) {
	usr, err := user.Current()
	if err != nil {
		log.Fatal(err)
	}

	if len(keypath) == 0 {
		keypath = usr.HomeDir + "/.ssh/id_rsa"
	}

	signer, err := loadPrivateKey(keypath)
	if err != nil {
		return nil, err
	}

	if len(username) == 0 {
		username = usr.Name
	}

	config := &ssh.ClientConfig{
		User: username,
		Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)},
	}

	return config, nil
}

func dialServer(hostname, port string, config *ssh.ClientConfig) (*ssh.Client, error) {
	if len(port) == 0 {
		port = os.Getenv("PORT")
		if len(port) == 0 {
			port = "22"
		}
	}
	conn, err := ssh.Dial("tcp", hostname+":"+port, config)
	if err != nil {
		return nil, err
	}
	return conn, nil
}

// SSHSession is a container for the pieces necessary to hold an SSH connection
// open in a goroutine
type SSHSession struct {
	hostname string
	config   *ssh.ClientConfig
	conn     *ssh.Client
	session  *ssh.Session
	error    error
}

func (s *SSHSession) init(hostname, username, keypath, port string) error {
	s.hostname = hostname

	// Instantiate config
	config, err := generateConfig(username, keypath)
	if err != nil {
		log.Fatal(err)
		return err
	}
	s.config = config

	// Establish connection
	conn, err := dialServer(hostname, port, config)
	if err != nil {
		log.Fatal(err)
		return err
	}
	s.conn = conn

	// Make a session
	session, err := conn.NewSession()
	if err != nil {
		log.Fatal(err)
		return err
	}
	s.session = session

	return nil
}

func (s *SSHSession) tearDown() {
	s.session.Close()
	s.conn.Close()
}

func (s *SSHSession) executeCmd(cmd string) string {
	// fmt.println("cmd: ", cmd)
	var stdoutBuf bytes.Buffer
	s.session.Stdout = &stdoutBuf
	s.session.Run(cmd)

	return stdoutBuf.String()
}

// GetSFTPClient returns an SFTP client from an existing SSHSession
func (s *SSHSession) GetSFTPClient() (*sftp.Client, error) {
	return sftp.NewClient(s.conn)
}

// SSHResponse contains the restul of running a command on a host via SSH
type SSHResponse struct {
	Hostname string
	Result   string
	Err      error
}

// ConnectToMachine takes host details and returns a connected SSHSession
func ConnectToMachine(address, username, keypath, port string) (*SSHSession, error) {
	session := &SSHSession{}
	err := session.init(address, username, keypath, port)
	if err != nil {
		return nil, err
	}

	return session, nil
}

// MapCmd takes the details for a command and maps it, via SSH, across a list
// of hosts
func MapCmd(hostnames HostGroup, username, keypath, port, command string, results chan SSHResponse) {
	for _, hostname := range hostnames {
		go func(hostname string) {
			response := SSHResponse{Hostname: hostname}
			session, err := ConnectToMachine(hostname, username, keypath, port)

			defer session.tearDown()

			if err != nil {
				fmt.Println("connection failed: ", err.Error())
				response.Err = err
			} else {
				result := session.executeCmd(command)
				response.Result = result
			}

			results <- response
		}(hostname)
	}
}

// MapScp takes the details for a file transfer and maps the file transfer
// across a list of hosts
func MapScp(hostnames HostGroup, username, keypath, port, localPath, remotePath string, results chan SSHResponse) {
	for _, hostname := range hostnames {
		go func(hostname string) {
			response := SSHResponse{Hostname: hostname}

			session, err := ConnectToMachine(hostname, username, keypath, port)
			defer session.tearDown()

			sftpc, err := session.GetSFTPClient()
			if err != nil {
				fmt.Println("failed to get sftp client: ", err.Error())
				response.Err = err
			}
			defer sftpc.Close()

			fmt.Println("PATH: ", filepath.Base(remotePath))
			// w := sftp.Walk(remotepath)
			// for w.Step() {
			// 	if w.Err() != nil {
			// 		continue
			// 	}
			// 	log.Println(w.Path())
			// }

			_, localFile := filepath.Split(localPath)
			remoteFile := remotePath + localFile
			f, err := sftpc.Create(remoteFile)
			if err != nil {
				fmt.Println("failed to create txt: ", err.Error())
				response.Err = err
			}

			lf, err := os.Open(localPath)
			if err != nil {
				return
			}
			defer lf.Close()

			buf := make([]byte, 1024)
			for {
				n, err := lf.Read(buf)
				if err != nil && err != io.EOF {
					panic(err)
				}
				if n == 0 {
					break
				}

				if _, err := f.Write(buf[:n]); err != nil {
					fmt.Println("failed to write: ", err.Error())
					response.Err = err
				}
			}
			results <- response
		}(hostname)
	}
}
