// Command adddirector creates a director account. There is no signup; an
// administrator runs this.
//
// Usage: adddirector -username NAME
//
// The password is read from the DIRECTOR_PASSWORD environment variable, or
// from one line of standard input if that is not set. It is never taken as
// an argument, so it does not appear in the process list or shell history.
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/conghan-tay/MonsterTheatre_01/server/internal/auth"
	"github.com/conghan-tay/MonsterTheatre_01/server/internal/config"
	"github.com/conghan-tay/MonsterTheatre_01/server/internal/db"
)

const maxUsernameLength = 80

func main() {
	username := flag.String("username", "", "the director's username")
	flag.Parse()

	id, err := run(context.Background(), *username)
	if err != nil {
		fmt.Fprintln(os.Stderr, "adddirector:", err)
		os.Exit(1)
	}
	fmt.Printf("created director %s (%s)\n", auth.NormalizeUsername(*username), id)
}

func run(ctx context.Context, username string) (string, error) {
	username = auth.NormalizeUsername(username)
	if username == "" || len(username) > maxUsernameLength {
		return "", fmt.Errorf("-username is required and at most %d characters", maxUsernameLength)
	}

	password, err := readPassword()
	if err != nil {
		return "", err
	}
	if len(password) < auth.MinPasswordLength {
		return "", fmt.Errorf("password must be at least %d characters", auth.MinPasswordLength)
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		return "", err
	}

	pool, _, err := db.OpenConfigured(ctx, config.Load())
	if err != nil {
		return "", err
	}
	defer pool.Close()

	id, err := (&auth.PGStore{Pool: pool}).CreateDirector(ctx, username, hash, time.Now())
	if errors.Is(err, auth.ErrUsernameTaken) {
		return "", fmt.Errorf("a director named %q already exists", username)
	}
	return id, err
}

func readPassword() (string, error) {
	if pw := os.Getenv("DIRECTOR_PASSWORD"); pw != "" {
		return pw, nil
	}
	fmt.Fprint(os.Stderr, "Password: ")
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && line == "" {
		return "", errors.New("no password given (set DIRECTOR_PASSWORD or type one on standard input)")
	}
	return strings.TrimRight(line, "\r\n"), nil
}
