package main

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"golang.org/x/sys/windows/registry"
	"gopkg.in/yaml.v2"
	"io"
	"io/ioutil"
	"os"
	"path/filepath"
	"regexp"
)

// Initialization of the constants for construction "CustomFiles" registry key
const (
	RegFilesHeadXML             = "<?xml version=\"1.0\" encoding=\"utf-16\"?>\n<ArrayOfApplicationFile xmlns:xsi=\"http://www.w3.org/2001/XMLSchema-instance\" xmlns:xsd=\"http://www.w3.org/2001/XMLSchema\">\n"
	RegFilesFileNameXML         = "  <ApplicationFile FileName=\""
	RegFilesRelativePathXML     = "\" RelativePath=\""
	RegFilesDataFileXML         = "\" DataFile=\""
	RegFilesEntryPointXML       = "\" EntryPoint=\""
	RegFilesIsMainConfigFileXML = "\" IsMainConfigFile=\""
	RegFilesOptionalXML         = "\" Optional=\""
	RegFilesGroupNameXML        = "\" GroupName=\""
	RegFilesTailXML             = "\" />\n"
	RegFilesEndingXML           = "</ArrayOfApplicationFile>"
)

// Store slice of registry kes and implement methods to interact with Windows registry.
type RegistryValues []RegistryValue

// TODO maybe replace with map
// Store pair key/value from one registry key.
type RegistryValue struct {
	Name string `yaml:"name"`
	Data string `yaml:"data"`
}

// Insert actual "CustomFiles" value into registry data slice.
func (rvs *RegistryValues) InsertActualCustomFilesValue(customFiles string) {
	for id, value := range *rvs {
		if value.Name == "CustomFiles" {
			(*rvs)[id].Data = customFiles
			return
		}
	}
	*rvs = append(*rvs, RegistryValue{
		Name: "CustomFiles",
		Data: customFiles,
	})
}

// Change or insert key "AddCustomFile" with value "True"
func (rvs *RegistryValues) InsertAddCustomFileTrueValue() {
	for id, value := range *rvs {
		if value.Name == "AddCustomFile" {
			(*rvs)[id].Data = "True"
			return
		}
	}
	*rvs = append(*rvs, RegistryValue{
		Name: "AddCustomFile",
		Data: "True",
	})
}

// Compare new and old registry data in key "CustomFiles" by FileName and RelativePath.
// If both equal, copy DataFile, EntryPoint, IsMainConfigFile, Optional and GroupName fields
// from old data to new data.
func (rvs *RegistryValues) AddManuallyAddedOptions(finalFilesList []CustomisationFile) error {
	// Get old data from XML
	oldFilesList := make([]CustomisationFile, 0, 128)
	findKey := false
	CFKeyID := 0
	for id, value := range *rvs {
		if value.Name != "CustomFiles" {
			continue
		}
		findKey = true
		var err error
		oldFilesList, err = ParseOldCustomFilesValue([]byte(value.Data))
		if err != nil {
			return err
		}
		CFKeyID = id
		break
	}

	// TODO - maybe replace custom error with just append "CustomFiles" key/value
	// Use default values if key "CustomFiles" not find in old data
	if !findKey {
		return ErrCustomFilesNotFound
	}

	// Compare data
	for id, newFile := range finalFilesList {
		for _, oldFile := range oldFilesList {
			if !(oldFile.FileName == newFile.FileName && oldFile.RelativePath == newFile.RelativePath) {
				continue
			}
			finalFilesList[id].DataFile = oldFile.DataFile
			finalFilesList[id].EntryPoint = oldFile.EntryPoint
			finalFilesList[id].IsMainConfigFile = oldFile.IsMainConfigFile
			finalFilesList[id].Optional = oldFile.Optional
			finalFilesList[id].GroupName = oldFile.GroupName
		}
	}

	// Construct and save new XML value for "CustomFiles" key
	(*rvs)[CFKeyID].Data = ConstructCustomFilesRegistryKey(finalFilesList)
	return nil
}

// Read previously saved registry key/value data from file.
// Automatically find latest .yaml file by name mask.
func ReadPreviouslySavedRegistryData(savedRegistryDirectory string) ([]byte, error) {
	// Read dir content.
	dirContent, err := ioutil.ReadDir(savedRegistryDirectory)
	if err != nil {
		return nil, err
	}
	var lastRegFile os.FileInfo

	// Sort out folders and not yaml files and find newer file.
	lastRegFile = nil
	reYAML := regexp.MustCompile(`.yaml$`)
	for _, file := range dirContent {
		if file.IsDir() {
			continue
		}
		if !reYAML.MatchString(file.Name()) {
			continue
		}
		if lastRegFile == nil {
			lastRegFile = file
			continue
		}
		if lastRegFile.ModTime().Before(file.ModTime()) {
			lastRegFile = file
		}
	}
	if lastRegFile == nil {
		return nil, ErrNoFilesFoundInFolderByPattern
	}

	// Read data from file and unmarshal yaml.
	fullFilePath := filepath.Join(savedRegistryDirectory, lastRegFile.Name())
	regFile, err := os.Open(fullFilePath)
	if err != nil {
		return nil, err
	}
	regBytes, err := ioutil.ReadAll(regFile)
	if err != nil {
		return nil, err
	}
	return regBytes, nil
}

// Unmarshal yaml row text into []RegistryValue
func UnmarshalRegistryData(regBytes []byte) ([]RegistryValue, error) {
	registryData := make([]RegistryValue, 0, 32)
	err := yaml.Unmarshal(regBytes, &registryData)
	if err != nil {
		return []RegistryValue{}, err
	}
	return registryData, nil
}

// Save keys/value pairs from registry into []RegistryValue.
func ReadRegistryData(registryDir string) ([]RegistryValue, error) {
	keyDir, err := registry.OpenKey(registry.CURRENT_USER, registryDir, registry.ENUMERATE_SUB_KEYS|registry.QUERY_VALUE)
	if err != nil {
		return nil, err
	}
	valueNames, err := keyDir.ReadValueNames(-1)
	if err != nil {
		return nil, err
	}
	regValues := make([]RegistryValue, 0, 32)
	for _, name := range valueNames {
		value, _, err := keyDir.GetStringValue(name)
		if err != nil {
			return nil, err
		}
		regValues = append(regValues, RegistryValue{Name: name, Data: value})
	}
	return regValues, nil
}

// Marshal registry data for save into file.
func MarshalRegistryData(regValues []RegistryValue) ([]byte, error) {
	registryBytes, err := yaml.Marshal(regValues)
	if err != nil {
		return nil, err
	}
	return registryBytes, nil
}

// Unmarshal XML from string and return CustomisationFile slice with filled
// FileName, RelativePath, DataFile, EntryPoint, IsMainConfigFile, Optional and GroupName values.
func ParseOldCustomFilesValue(oldCustomFiles []byte) ([]CustomisationFile, error) {
	var oldData XMLCustomFiles
	decoderXML := xml.NewDecoder(bytes.NewReader(oldCustomFiles))
	decoderXML.CharsetReader = IdentReader
	err := decoderXML.Decode(&oldData)
	if err != nil {
		return []CustomisationFile{}, err
	}
	return oldData.ApplicationFile, nil
}

// Used in parse XML to avoid encoding mismatch.
func IdentReader(encoding string, input io.Reader) (io.Reader, error) {
	return input, nil
}

// Construct XML with format valid for DM WDE.
func ConstructCustomFilesRegistryKey(customFilesList []CustomisationFile) string {
	result := RegFilesHeadXML
	for _, file := range customFilesList {
		result = fmt.Sprint(result, ConstructLineForCustomFilesRegistryKey(file))
	}
	return fmt.Sprint(result, RegFilesEndingXML)
}

// Convert variable of CustomisationFile type into string for registry key.
func ConstructLineForCustomFilesRegistryKey(cf CustomisationFile) string {
	return fmt.Sprint(
		RegFilesFileNameXML,
		cf.FileName,
		RegFilesRelativePathXML,
		cf.RelativePath,
		RegFilesDataFileXML,
		cf.EntryPoint,
		RegFilesEntryPointXML,
		cf.IsMainConfigFile,
		RegFilesIsMainConfigFileXML,
		cf.IsMainConfigFile,
		RegFilesOptionalXML,
		cf.Optional,
		RegFilesGroupNameXML,
		cf.GroupName,
		RegFilesTailXML,
	)
}

// Write data into registry.
func WriteToRegistry(registryData []RegistryValue) error {
	// Open directory key DMRegistryDir with write privileges.
	keyDir, _, err := registry.CreateKey(registry.CURRENT_USER, DMRegistryDir, registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		return err
	}
	// Write or rewrite child keys values
	for _, key := range registryData {
		if err := keyDir.SetStringValue(key.Name, key.Data); err != nil {
			return err
		}
	}
	if err := keyDir.Close(); err != nil {
		return err
	}
	return nil
}
