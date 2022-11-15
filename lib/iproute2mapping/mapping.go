package iproute2mapping

import (
	"os"
	"bufio"
	"strings"
	"strconv"
	"fmt"
)

// Storage for mapping
var ByName  = make(map[int]map[string]int)
var ById  	= make(map[int]map[int]string)

// Mapping types
const (
	PROTOCOL = iota
	TABLE
)

// Paths
var filePaths = map[int]string{
	PROTOCOL: 	"/etc/iproute2/rt_protos",
	TABLE:		"/etc/iproute2/rt_tables",
}

// Export error-check
var Errors []error;

func init() {
	var err error
	for mapType, filePath := range filePaths{
		ByName[mapType], ById[mapType], err = readFromFile(filePath)
		if(err != nil){
			Errors = []error{
				fmt.Errorf("failed reading iproute2 mapping-file '%s': %s", filePath, err),
				};
		}
	}
}

func readFromFile(filePath string) (mapByName map[string]int, mapById map[int]string, err error){
	file, err := os.Open(filePath)
	if(err != nil){
		return nil, nil, err;
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
    scanner.Split(bufio.ScanLines)
  
	mapByName = make(map[string]int)
	mapById =  make(map[int]string)

	// Go through file line-by-line
    for scanner.Scan() {
		text := scanner.Text()
		if(strings.HasPrefix(text, "#") || text == ""){
			continue
		}

		id, name, err := getMappingFromText(text)
		if(err != nil){
			// Only warn as we can continue processing the file
			Errors = append(Errors, 
				fmt.Errorf("mappig-data invalid '%s': %s", text, err),
			)
			continue
		}

		mapByName[name] = id
		mapById[id] = name
    }

	return
}

func getMappingFromText(text string) (int, string, error) {
	// Split and read/convert data
	data := strings.Split(text, "\t")

	id, err := strconv.Atoi(data[0])
	if(err != nil){
		return 0, "", err
	}

	return id, data[1], nil
}

// Try getting an id from a name (or a string containing an id) with a specified type
func TryGetId(mappingType int, name string) (int, error){
	// Try to convert name to id
	id, err := strconv.Atoi(name)	
	if(err != nil){	// name given -> Convert to id
		var found bool
		id, found = ByName[mappingType][name]
		if(!found){
			return 0, fmt.Errorf("no id found from name")
		}
	}
	return id, nil
}
