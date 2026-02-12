package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/google/shlex"
	"github.com/urfave/cli/v2"
)

type inventoryList struct {
	Meta inventoryMeta `json:"_meta"`
}

type inventoryMeta struct {
	Hostvars map[string]map[string]any `json:"hostvars"`
}

type hostConfig struct {
	Name       string
	Host       string
	User       string
	KeyFile    string
	CommonArgs string
}

type commandSpec struct {
	Raw  string
	Bin  string
	Args []string
}

func main() {
	app := &cli.App{
		Name:  "anssh",
		Usage: "SSH/tunnel helper for Ansible inventory",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:    "inventory",
				Aliases: []string{"i"},
				EnvVars: []string{"ANSSH_INVENTORY"},
				Usage:   "Path to inventory hosts.yml",
			},
		},
		Action: runDefaultSSH,
		Commands: []*cli.Command{
			{
				Name:      "tunnel",
				Usage:     "Create SSH local tunnel with auto-reconnect",
				ArgsUsage: "<target> <local_port:remote_port>",
				Action:    runTunnel,
			},
			{
				Name:      "exec",
				Usage:     "Execute command on remote host",
				ArgsUsage: "<target> <command>",
				Action:    runExec,
			},
			{
				Name:   "list",
				Usage:  "List hosts from inventory",
				Action: runList,
			},
			{
				Name:   "doctor",
				Usage:  "Check required binaries in PATH",
				Action: runDoctor,
			},
		},
	}

	if err := app.Run(os.Args); err != nil {
		var codeErr cli.ExitCoder
		if errors.As(err, &codeErr) {
			os.Exit(codeErr.ExitCode())
		}
		fmt.Fprintln(os.Stderr, err.Error())
		os.Exit(1)
	}
}

func runDefaultSSH(c *cli.Context) error {
	if c.Args().Len() == 0 {
		return cli.ShowAppHelp(c)
	}

	target := c.Args().Get(0)
	extraArgs := c.Args().Slice()[1:]

	hosts, err := loadHosts(c.String("inventory"))
	if err != nil {
		return err
	}
	hc, err := resolveHost(hosts, target)
	if err != nil {
		return err
	}

	args, err := sshArgs(hc)
	if err != nil {
		return err
	}
	args = append(args, extraArgs...)
	args = append(args, destination(hc))

	return runInteractiveSSH(args)
}

func runExec(c *cli.Context) error {
	if c.Args().Len() < 2 {
		return cli.Exit("Usage: anssh exec <target> <command>", 2)
	}

	target := c.Args().Get(0)
	remoteCmd := c.Args().Slice()[1:]

	hosts, err := loadHosts(c.String("inventory"))
	if err != nil {
		return err
	}
	hc, err := resolveHost(hosts, target)
	if err != nil {
		return err
	}

	args, err := sshArgs(hc)
	if err != nil {
		return err
	}
	args = append(args, destination(hc))
	args = append(args, remoteCmd...)

	return runInteractiveSSH(args)
}

func runTunnel(c *cli.Context) error {
	if c.Args().Len() < 2 {
		return cli.Exit("Usage: anssh tunnel <target> <local_port:remote_port>", 2)
	}

	target := c.Args().Get(0)
	mapping := c.Args().Get(1)

	localPort, remotePort, err := parsePortMapping(mapping)
	if err != nil {
		return cli.Exit(err.Error(), 2)
	}

	hosts, err := loadHosts(c.String("inventory"))
	if err != nil {
		return err
	}
	hc, err := resolveHost(hosts, target)
	if err != nil {
		return err
	}

	baseArgs, err := sshArgs(hc)
	if err != nil {
		return err
	}

	tunnelArgs := append([]string{}, baseArgs...)
	tunnelArgs = append(tunnelArgs, "-L", fmt.Sprintf("%d:localhost:%d", localPort, remotePort), "-N", destination(hc))

	ctx, stop := signal.NotifyContext(c.Context, os.Interrupt, syscall.SIGTERM)
	defer stop()

	attempt := 0
	for {
		cmd := exec.CommandContext(ctx, "ssh", tunnelArgs...)
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr

		err := cmd.Run()
		if ctx.Err() != nil {
			return nil
		}

		attempt++
		wait := backoff(attempt)
		if err == nil {
			fmt.Fprintf(os.Stderr, "Tunnel closed. Reconnecting in %s...\n", wait)
		} else {
			fmt.Fprintf(os.Stderr, "Tunnel disconnected (%v). Reconnecting in %s...\n", err, wait)
		}

		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
	}
}

func runList(c *cli.Context) error {
	hosts, err := loadHosts(c.String("inventory"))
	if err != nil {
		return err
	}

	names := make([]string, 0, len(hosts))
	for name := range hosts {
		names = append(names, name)
	}
	sort.Strings(names)

	tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tHOST\tUSER\tPROXY")
	for _, name := range names {
		hc := hosts[name]
		proxy := "-"
		if strings.TrimSpace(hc.CommonArgs) != "" {
			proxy = "yes"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", hc.Name, hc.Host, hc.User, proxy)
	}
	return tw.Flush()
}

func runDoctor(c *cli.Context) error {
	ansibleCmd, err := ansibleInventoryCommand()
	if err != nil {
		return cli.Exit(err.Error(), 2)
	}

	checks := []commandSpec{
		{Raw: "ssh", Bin: "ssh"},
		ansibleCmd,
	}

	tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "COMMAND\tSTATUS\tPATH")

	hasMissing := false
	for _, check := range checks {
		path, err := exec.LookPath(check.Bin)
		if err != nil {
			hasMissing = true
			fmt.Fprintf(tw, "%s\tmissing\t-\n", check.Raw)
			continue
		}
		fmt.Fprintf(tw, "%s\tok\t%s\n", check.Raw, path)
	}

	if err := tw.Flush(); err != nil {
		return err
	}

	if hasMissing {
		return cli.Exit("doctor failed: install missing binaries", 1)
	}

	return nil
}

func loadHosts(inventoryPath string) (map[string]hostConfig, error) {
	path := strings.TrimSpace(inventoryPath)
	if path == "" {
		return nil, cli.Exit("inventory is required: pass --inventory or set ANSSH_INVENTORY", 2)
	}

	ansibleCmd, err := ansibleInventoryCommand()
	if err != nil {
		return nil, cli.Exit(err.Error(), 2)
	}

	args := append([]string{}, ansibleCmd.Args...)
	args = append(args, "-i", path, "--list")

	cmd := exec.Command(ansibleCmd.Bin, args...)
	raw, err := cmd.CombinedOutput()
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return nil, cli.Exit(fmt.Sprintf("%s binary not found in PATH", ansibleCmd.Bin), 127)
		}

		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			msg := strings.TrimSpace(string(raw))
			if msg == "" {
				msg = exitErr.Error()
			}
			return nil, cli.Exit(fmt.Sprintf("%s failed: %s", ansibleCmd.Raw, msg), exitErr.ExitCode())
		}

		return nil, fmt.Errorf("run %s: %w", ansibleCmd.Raw, err)
	}

	var inv inventoryList
	if err := json.Unmarshal(raw, &inv); err != nil {
		return nil, fmt.Errorf("parse ansible-inventory output: %w", err)
	}

	hosts := make(map[string]hostConfig, len(inv.Meta.Hostvars))
	for name, vars := range inv.Meta.Hostvars {
		hosts[name] = hostFromVars(name, toStringMap(vars))
	}

	return hosts, nil
}

func ansibleInventoryCommand() (commandSpec, error) {
	raw := strings.TrimSpace(os.Getenv("ANSSH_ANSIBLE_INVENTORY_CMD"))
	if raw == "" {
		return commandSpec{Raw: "ansible-inventory", Bin: "ansible-inventory"}, nil
	}

	parts, err := shlex.Split(raw)
	if err != nil {
		return commandSpec{}, fmt.Errorf("parse ANSSH_ANSIBLE_INVENTORY_CMD: %w", err)
	}
	if len(parts) == 0 {
		return commandSpec{}, fmt.Errorf("ANSSH_ANSIBLE_INVENTORY_CMD is empty")
	}

	return commandSpec{Raw: raw, Bin: parts[0], Args: parts[1:]}, nil
}

func hostFromVars(name string, vars map[string]string) hostConfig {
	host := strings.TrimSpace(vars["ansible_host"])
	if host == "" {
		host = name
	}
	return hostConfig{
		Name:       name,
		Host:       host,
		User:       strings.TrimSpace(vars["ansible_user"]),
		KeyFile:    strings.TrimSpace(vars["ansible_ssh_private_key_file"]),
		CommonArgs: strings.TrimSpace(vars["ansible_ssh_common_args"]),
	}
}

func toStringMap(input map[string]any) map[string]string {
	out := map[string]string{}
	for k, v := range input {
		out[k] = fmt.Sprint(v)
	}
	return out
}

func resolveHost(hosts map[string]hostConfig, target string) (hostConfig, error) {
	hc, ok := hosts[target]
	if !ok {
		return hostConfig{}, cli.Exit(fmt.Sprintf("host %q not found in inventory", target), 2)
	}
	return hc, nil
}

func sshArgs(hc hostConfig) ([]string, error) {
	args := []string{}
	if hc.KeyFile != "" {
		args = append(args, "-i", hc.KeyFile)
	}
	if strings.TrimSpace(hc.CommonArgs) != "" {
		split, err := shlex.Split(hc.CommonArgs)
		if err != nil {
			return nil, fmt.Errorf("parse ansible_ssh_common_args for %q: %w", hc.Name, err)
		}
		args = append(args, split...)
	}
	return args, nil
}

func destination(hc hostConfig) string {
	if hc.User == "" {
		return hc.Host
	}
	return hc.User + "@" + hc.Host
}

func parsePortMapping(s string) (int, int, error) {
	parts := strings.Split(s, ":")
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("invalid port mapping %q, expected local:remote", s)
	}

	localPort, err := strconv.Atoi(parts[0])
	if err != nil || localPort <= 0 || localPort > 65535 {
		return 0, 0, fmt.Errorf("invalid local port %q", parts[0])
	}

	remotePort, err := strconv.Atoi(parts[1])
	if err != nil || remotePort <= 0 || remotePort > 65535 {
		return 0, 0, fmt.Errorf("invalid remote port %q", parts[1])
	}

	return localPort, remotePort, nil
}

func backoff(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	seconds := 1 << (attempt - 1)
	if seconds > 30 {
		seconds = 30
	}
	return time.Duration(seconds) * time.Second
}

func runInteractiveSSH(args []string) error {
	cmd := exec.Command("ssh", args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return cli.Exit("", exitErr.ExitCode())
		}
		if errors.Is(err, exec.ErrNotFound) {
			return cli.Exit("ssh binary not found in PATH", 127)
		}
		return err
	}

	return nil
}
