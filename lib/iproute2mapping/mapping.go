package iproute2mapping

import (
	"bufio"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Storage for mapping
var ByName = make(map[int]map[string]int)
var ById = make(map[int]map[int]string)

// Mapping types
const (
	PROTOCOL = iota
	TABLE
)

// Mapping file of each type, relative to the directories in roots.
// A directory <file>.d next to it may hold additional *.conf files.
var fileNames = map[int]string{
	PROTOCOL: "rt_protos",
	TABLE:    "rt_tables",
}

// Directories holding the mapping files. Later entries override earlier ones,
// like iproute2 lets /etc override the distribution defaults in /usr/share.
var roots = []string{"/usr/share/iproute2", "/etc/iproute2"}

// Export error-check
var Errors []error

func init() {
	for mapType, fileName := range fileNames {
		m := load(roots, fileName)
		ByName[mapType], ById[mapType] = m.byName, m.byId
		Errors = append(Errors, m.errs...)
	}
}

type mapping struct {
	byName map[string]int
	byId   map[int]string
	errs   []error
}

// load reads the mapping file and its .d directory from every root.
// Roots or files that do not exist are skipped, unless none exists at all.
func load(roots []string, fileName string) *mapping {
	m := &mapping{
		byName: make(map[string]int),
		byId:   make(map[int]string),
	}

	found := false
	for _, root := range roots {
		paths := []string{filepath.Join(root, fileName)}
		confs, err := filepath.Glob(filepath.Join(root, fileName+".d", "*.conf"))
		if err != nil {
			m.errs = append(m.errs, err)
		}
		paths = append(paths, confs...)

		for _, path := range paths {
			err := m.readFile(path)
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			found = true
			if err != nil {
				m.errs = append(m.errs, fmt.Errorf("failed reading mapping-file '%s': %w", path, err))
			}
		}
	}

	if !found {
		m.errs = append(m.errs, fmt.Errorf("no mapping-file '%s' found in %s", fileName, strings.Join(roots, ", ")))
	}
	return m
}

// readFile adds all valid entries of the file to the mapping.
// Invalid lines are added to the errors and skipped.
func (m *mapping) readFile(filePath string) error {
	file, err := os.Open(filePath)
	if err != nil {
		return err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)

	// Go through file line-by-line
	for scanner.Scan() {
		text := strings.TrimSpace(scanner.Text())
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}

		id, name, err := getMappingFromText(text)
		if err != nil {
			// Only warn as we can continue processing the file
			m.errs = append(m.errs,
				fmt.Errorf("mapping-data invalid in '%s': '%s': %w", filePath, text, err),
			)
			continue
		}

		m.byName[name] = id
		m.byId[id] = name
	}

	return scanner.Err()
}

// getMappingFromText parses "<id> <name>" with any whitespace in between.
// The id is decimal or hexadecimal with a 0x prefix, anything after the name is ignored.
func getMappingFromText(text string) (int, string, error) {
	data := strings.Fields(text)
	if len(data) < 2 {
		return 0, "", fmt.Errorf("expected '<id> <name>'")
	}

	idStr, base := data[0], 10
	if len(idStr) > 2 && (idStr[:2] == "0x" || idStr[:2] == "0X") {
		idStr, base = idStr[2:], 16
	}
	// 31 bits so that the id also fits into an int on 32-bit platforms
	id, err := strconv.ParseUint(idStr, base, 31)
	if err != nil {
		return 0, "", err
	}

	return int(id), data[1], nil
}

// Try getting an id from a name (or a string containing an id) with a specified type
func TryGetId(mappingType int, name string) (int, error) {
	// Try to convert name to id
	id, err := strconv.Atoi(name)
	if err != nil { // name given -> Convert to id
		var found bool
		id, found = ByName[mappingType][name]
		if !found {
			return 0, fmt.Errorf("no id found from name")
		}
	}
	return id, nil
}
