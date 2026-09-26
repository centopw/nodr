package cli

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/centopw/nodr/internal/authn"
)

func (a *app) authCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "auth",
		Short: "Manage the local administrator account",
	}
	cmd.AddCommand(a.authCreateAdminCommand())
	return cmd
}

func (a *app) authCreateAdminCommand() *cobra.Command {
	var passwordStdin bool
	cmd := &cobra.Command{
		Use:   "create-admin",
		Short: "Create or replace the single local administrator account",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			loaded, err := a.mustLoad()
			if err != nil {
				return err
			}
			var username, password string
			if passwordStdin {
				reader := bufio.NewReader(a.stdin)
				username, err = readLine(reader, "Username: ", a.stdout)
				if err != nil {
					return err
				}
				password, err = readLine(reader, "", nil)
				if err != nil {
					return err
				}
			} else {
				if !a.interactive {
					return usageError{errors.New("stdin is not a terminal; pass --password-stdin to create an admin account non-interactively")}
				}
				username, err = readLine(bufio.NewReader(a.stdin), "Username: ", a.stdout)
				if err != nil {
					return err
				}
				password, err = readPasswordTwice(a.stdin, a.stdout)
				if err != nil {
					return err
				}
			}
			if err := os.MkdirAll(filepath.Join(loaded.ws.Root, ".nodr"), 0700); err != nil {
				return err
			}
			store, err := authn.Open(filepath.Join(loaded.ws.Root, ".nodr", "authn.db"))
			if err != nil {
				return err
			}
			defer store.Close()
			if err := store.CreateAccount(cmd.Context(), username, password); err != nil {
				return err
			}
			fmt.Fprintf(a.stdout, "created administrator account %q\n", username)
			return nil
		},
	}
	cmd.Flags().BoolVar(&passwordStdin, "password-stdin", false, "read username then password as two lines from stdin, for scripted setup")
	return cmd
}

func readLine(reader *bufio.Reader, prompt string, out io.Writer) (string, error) {
	if prompt != "" && out != nil {
		fmt.Fprint(out, prompt)
	}
	line, err := reader.ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

func readPasswordTwice(stdin io.Reader, stdout io.Writer) (string, error) {
	f, ok := stdin.(*os.File)
	if !ok || !term.IsTerminal(int(f.Fd())) {
		return "", usageError{errors.New("stdin is not a terminal; pass --password-stdin to create an admin account non-interactively")}
	}
	fmt.Fprint(stdout, "Password: ")
	p1, err := term.ReadPassword(int(f.Fd()))
	fmt.Fprintln(stdout)
	if err != nil {
		return "", err
	}
	fmt.Fprint(stdout, "Confirm password: ")
	p2, err := term.ReadPassword(int(f.Fd()))
	fmt.Fprintln(stdout)
	if err != nil {
		return "", err
	}
	if string(p1) != string(p2) {
		return "", errors.New("passwords do not match")
	}
	return string(p1), nil
}
