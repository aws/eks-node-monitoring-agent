package collect

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"strings"
)

type NFTables struct{}

var _ Collector = (*NFTables)(nil)

func (n NFTables) Collect(acc *Accessor) error {
	if !n.isNftAvailable(acc) {
		acc.logger.Info("nft binary not available, skipping NFTables log collection")
		return nil
	}

	var merr error

	// First, list all tables
	tables, err := n.listTables(acc)
	if err != nil {
		return fmt.Errorf("error listing nftables: %w", err)
	}

	// Dump each table's content
	for _, table := range tables {
		filename := fmt.Sprintf("networking/nftables-%s-%s.txt", table.family, table.name)
		merr = errors.Join(merr, n.collectRules(acc, []string{"nft", "list", "table", table.family, table.name}, filename))
	}

	return merr
}

type nftable struct {
	family string
	name   string
}

func (n NFTables) listTables(acc *Accessor) ([]nftable, error) {
	output, err := acc.Command("nft", "list", "tables").CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("error running 'nft list tables': %w", err)
	}

	var tables []nftable
	sc := bufio.NewScanner(bytes.NewReader(output))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if len(line) == 0 {
			continue
		}
		// Parse lines like "table ip filter" or "table ip6 nat"
		parts := strings.Fields(line)
		if len(parts) >= 3 && parts[0] == "table" {
			tables = append(tables, nftable{
				family: parts[1],
				name:   parts[2],
			})
		}
	}

	return tables, nil
}

func (n NFTables) isNftAvailable(acc *Accessor) bool {
	// Try to run 'nft --version' to check if binary exists and is functional
	cmd := acc.Command("nft", "--version")
	err := cmd.Run()
	return err == nil
}

func (n NFTables) collectRules(acc *Accessor, cmd []string, filename string) error {
	output, err := acc.Command(cmd[0], cmd[1:]...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("error running %q, %w", cmd, err)
	}

	return acc.WriteOutput(filename, output)
}
