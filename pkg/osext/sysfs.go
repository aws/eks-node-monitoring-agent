package osext

import (
	"os"
	"strconv"
	"strings"
)

func ReadInt(path string) (int, error) {
	counterData, err := os.ReadFile(path)
	if err != nil {
		return -1, err
	}
	counterValue, err := strconv.Atoi(strings.TrimSpace(string(counterData)))
	if err != nil {
		return -1, err
	}
	return counterValue, nil
}
