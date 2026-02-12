package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExpandKnownTemplatesInventoryDir(t *testing.T) {
	inventoryPath := filepath.Join(t.TempDir(), "inventory", "hosts.yml")

	vars := map[string]string{
		"ansible_ssh_private_key_file": "{{ inventory_dir }}/../@secrets/node-access-key",
	}

	out := expandKnownTemplates(vars, inventoryPath)
	absInventoryDir, err := filepath.Abs(filepath.Dir(inventoryPath))
	if err != nil {
		t.Fatalf("resolve abs inventory dir: %v", err)
	}

	expected := absInventoryDir + "/../@secrets/node-access-key"
	if out["ansible_ssh_private_key_file"] != expected {
		t.Fatalf("unexpected expanded value: got %q want %q", out["ansible_ssh_private_key_file"], expected)
	}
}

func TestLoadHostsExpandsInventoryDir(t *testing.T) {
	tmp := t.TempDir()
	inventoryPath := filepath.Join(tmp, "inventory", "hosts.yml")

	if err := os.MkdirAll(filepath.Dir(inventoryPath), 0o755); err != nil {
		t.Fatalf("create inventory dir: %v", err)
	}

	scriptPath := filepath.Join(tmp, "fake-ansible-inventory.sh")
	script := `#!/bin/sh
cat <<'EOF'
{"_meta":{"hostvars":{"node1":{"ansible_host":"10.0.0.1","ansible_user":"ubuntu","ansible_ssh_private_key_file":"{{ inventory_dir }}/../@secrets/node-access-key"}}}}
EOF
`
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake ansible-inventory script: %v", err)
	}

	t.Setenv("ANSSH_ANSIBLE_INVENTORY_CMD", scriptPath)

	hosts, err := loadHosts(inventoryPath)
	if err != nil {
		t.Fatalf("load hosts: %v", err)
	}

	hc, ok := hosts["node1"]
	if !ok {
		t.Fatalf("node1 host not found")
	}

	absInventoryDir, err := filepath.Abs(filepath.Dir(inventoryPath))
	if err != nil {
		t.Fatalf("resolve abs inventory dir: %v", err)
	}
	expected := absInventoryDir + "/../@secrets/node-access-key"
	if hc.KeyFile != expected {
		t.Fatalf("unexpected key file: got %q want %q", hc.KeyFile, expected)
	}
}
