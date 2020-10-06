package main

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"github.com/gonutz/w32"
	"golang.org/x/sys/windows/registry"
	"gopkg.in/yaml.v2"
	"io"
	"io/ioutil"
	"log"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"sort"
	"time"
)

const (
	programVersion   string = "2.0.0.0"                                   // Program version.
	confFile         string = "config.yaml"                               // Configuration file name.
	logHistLayout    string = "2006.01.02_150405"                         // Layout for "log" and "history" filenames time appending.
	WDESubfolder     string = "InteractionWorkspace"                      // WDE subfolder in MainCfgYAML.WDEFolder.
	DMSubfolder      string = "InteractionWorkspaceDeploymentManager"     // WDE Deployment Manager subfolder in MainCfgYAML.WDEFolder.
	DMExecutableName string = "InteractionWorkspaceDeploymentManager.exe" // WDE Deployment Manager executable.
	DMRegistryDir    string = `Software\Genesys\DeploymentManager`        // WDE Deployment Manager registry directory.
	SavedRegFolder   string = "Registry"                                  // Folder name for saved registry data.
	RegFileName      string = "DM_Registry_values_"                       // Name prefix for saved registry files.
	HistoryFileName  string = "WDE_History_"                              // Name prefix for history files.

	// Initialization of the constants for construction "CustomFiles" registry key
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

// For data from "config.yaml" file.
type MainCfgYAML struct {
	WDEFolder            string `yaml:"WDEFolder"`
	CustomizationsFolder string `yaml:"CustomizationsFolder"`
}

// Store file version in decimal.
type FileVersion struct {
	full uint64
	v1   uint64
	v2   uint64
	v3   uint64
	v4   uint64
}

// Store fined customisation files with data needed for filtering, coping
// and propagate values for fill "CustomFiles" registry key.
// Also used for parse data from previously saved "CustomFiles" registry key.
type CustomizationFile struct {
	FileName         string      `xml:"FileName,attr"`         // For registry key. File name.
	RelativePath     string      `xml:"RelativePath,attr"`     // For registry key. Relative path. Contains relative file directory
	DataFile         string      `xml:"DataFile,attr"`         // For registry key. By default "false". Can be "true" (not implemented).
	EntryPoint       string      `xml:"EntryPoint,attr"`       // For registry key. By default "false". Can be "true" (not implemented).
	IsMainConfigFile string      `xml:"IsMainConfigFile,attr"` // For registry key. By default "false". Can be "true" (not implemented).
	Optional         string      `xml:"Optional,attr"`         // For registry key. By default "false". Can be "true" (not implemented).
	GroupName        string      `xml:"GroupName,attr"`        // For registry key. Can be custom, also can be empty.
	SourcePath       string      // Full path to source file.
	LastWriteTime    time.Time   // Last write time for current file.
	Version          FileVersion // Version of file. If not collected use zero value.
}

// TODO maybe replace with map
// Store pair key/value from one registry key.
type RegistryValue struct {
	Name string `yaml:"name"`
	Data string `yaml:"data"`
}

// Store slice of registry kes and implement methods.
type RegistryValues []RegistryValue

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
func (rvs *RegistryValues) AddManuallyAddedOptions(finalFilesList []CustomizationFile) error {
	// Get old data from XML
	oldFilesList := make([]CustomizationFile, 0, 128)
	findKey := false
	CFKeyID := 0
	for id, value := range *rvs {
		if value.Name != "CustomFiles" {
			continue
		}
		findKey = true
		var err error
		oldFilesList, err = ParseOldCustomFilesValue(value.Data)
		if err != nil {
			log.Println(err)
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

var ErrCustomFilesNotFound = fmt.Errorf("not found CustomFiles key in old registry data \"RegistryValues\"")

// Implement methods needed by sort.Sort() for custom sort rules.
// Used in sorting files.
type FileInfoSlice []os.FileInfo

func (fis FileInfoSlice) Len() int {
	return len(fis)
}

func (fis FileInfoSlice) Less(i, j int) bool {
	return fis[i].ModTime().Before(fis[j].ModTime())
}

func (fis FileInfoSlice) Swap(i, j int) {
	fis[i], fis[j] = fis[j], fis[i]
}

// Struct for unmarshal XML from "CustomFiles" key
type XMLCustomFiles struct {
	XMLName         xml.Name            `xml:"ArrayOfApplicationFile"`
	ApplicationFile []CustomizationFile `xml:"ApplicationFile"`
}

// TODO - add logging logic (log to file, unified log format, add new log events)
// TODO - test folders with spaces
func main() {
	// Fill program start information.
	startTime := time.Now()                            //Save start time.
	startTimeString := startTime.Format(logHistLayout) //Get string from startTime.
	programDirectory, _ := os.Getwd()                  //Save program folder.

	// Read configuration from file.
	mainConfig, err := ReadConfigFromYAMLFile()
	if err != nil {
		log.Printf("[FAIL    ] main - can't read confFile `%v`", confFile)
		log.Println(err)
		return
	}

	// Get customisation folders list.
	foldersWithCustomisations, err := GetCustomisationFoldersList(mainConfig.CustomizationsFolder)
	if err != nil {
		log.Println("[FAIL    ] main - can't get customisation folders list")
		log.Println(err)
		return
	}

	// Get all files from  all customisation folders.
	rowFilesList := make([]CustomizationFile, 0, 128)
	for _, folder := range foldersWithCustomisations {
		scanPath := filepath.Join(mainConfig.CustomizationsFolder, folder)
		tmpFilesList, err := CollectCustomisationFiles(scanPath, scanPath)
		if err != nil {
			log.Println("[FAIL    ] main - can't get customisation files list")
			log.Println(err)
			return
		}
		rowFilesList = append(rowFilesList, tmpFilesList...)
	}

	// Filtering redundant and older files.
	// Get filtered files list and statuses of all original files.
	finalFilesList, rowFilesStatuses := ValidateCollectedFiles(rowFilesList)

	// Write into history file initiator user name, program version
	// and all original files with statuses.
	// History file start in parallel process, may fail without affect on main process,
	// but can prevent close program if write process took longer than main process.
	historyWritingEnd := make(chan bool)
	historyFileFullPath := filepath.Join(
		programDirectory,
		"History",
		fmt.Sprint(HistoryFileName, startTimeString, ".log"),
	)
	go WriteHistoryFile(
		rowFilesList,
		mainConfig.CustomizationsFolder,
		rowFilesStatuses,
		foldersWithCustomisations,
		historyFileFullPath,
		historyWritingEnd,
	)

	// Copy all filtered files into WDE folder.
	err = CopyCustomisationFiles(finalFilesList, filepath.Join(mainConfig.WDEFolder, WDESubfolder))

	// TODO - avoid reading the file just written
	// Read previously saved registry data.
	// If there are no files to read, save the new registry data to a file and read from it.
	savedRegistryFolder := filepath.Join(programDirectory, SavedRegFolder)
	var previousRegData RegistryValues
	previousRegData, err = ReadPreviouslySavedRegistryData(savedRegistryFolder)
	if err != nil {
		if err != ErrFolderContainNoFiles {
			log.Println(err)
			return
		}
		registryPreBytes, err := SaveRegistryKeys(DMRegistryDir)
		if err != nil {
			log.Println(err)
			return
		}
		registryFileFullPath := filepath.Join(
			programDirectory,
			SavedRegFolder,
			fmt.Sprint(RegFileName, "INITIALISATION_", startTimeString, ".yaml"),
		)
		err = SaveBytesIntoFile(registryFileFullPath, registryPreBytes)
		if err != nil {
			log.Println(err)
			return
		}
		previousRegData, err = ReadPreviouslySavedRegistryData(savedRegistryFolder)
		if err != nil {
			log.Println(err)
			return
		}
	}

	//Update data previously saved from registry and now read from file.
	previousRegData.InsertAddCustomFileTrueValue()                // Force set "AddCustomFile" with "True"
	err = previousRegData.AddManuallyAddedOptions(finalFilesList) // Combine manually added options and new collected files.
	if err != nil {
		if err == ErrCustomFilesNotFound {
			previousRegData.InsertActualCustomFilesValue(ConstructCustomFilesRegistryKey(finalFilesList))
		} else {
			log.Println(err)
		}
	}

	// Write collected from file and updated data into registry.
	err = WriteToRegistry(previousRegData)
	if err != nil {
		log.Println(err)
		return
	}

	// Run WDE Deployment Manager and wait while it stop.
	log.Printf("Run WDE Deployment Manager")
	err = RunAndWaitStop(filepath.Join(mainConfig.WDEFolder, DMSubfolder, DMExecutableName))
	if err != nil {
		log.Println(err)
		return
	}
	log.Printf("WDE Deployment Manager stopped")

	// Save actual registry data into file.
	registryBytes, err := SaveRegistryKeys(DMRegistryDir)
	if err != nil {
		log.Println(err)
		return
	}
	registryFileFullPath := filepath.Join(
		programDirectory,
		SavedRegFolder,
		fmt.Sprint(RegFileName, startTimeString, ".yaml"),
	)
	err = SaveBytesIntoFile(registryFileFullPath, registryBytes)
	if err != nil {
		log.Println(err)
		return
	}

	// Clean old registry files. Preserve last 5 files for backup purposes.
	err = ClearOldFiles(filepath.Join(programDirectory, SavedRegFolder), RegFileName, 5)
	if err != nil {
		log.Printf("can't clear old registry files - '%v'", err)
	}

	// Wait for the history file to finish writing end exit program.
	log.Printf("History writing stopped '%v'", <-historyWritingEnd)
	log.Println("[SUCCESS ] main")
}

// TODO - change oldCustomFiles argument type from string to []byte
// Unmarshal XML from string and return CustomizationFile slice with filled
// FileName, RelativePath, DataFile, EntryPoint, IsMainConfigFile, Optional and GroupName values.
func ParseOldCustomFilesValue(oldCustomFiles string) ([]CustomizationFile, error) {
	var oldData XMLCustomFiles
	decoderXML := xml.NewDecoder(bytes.NewReader([]byte(oldCustomFiles)))
	decoderXML.CharsetReader = IdentReader
	err := decoderXML.Decode(&oldData)
	if err != nil {
		return []CustomizationFile{}, err
	}
	return oldData.ApplicationFile, nil
}

// Used in parse XML to avoid encoding mismatch.
func IdentReader(encoding string, input io.Reader) (io.Reader, error) {
	return input, nil
}

// Clear files in specified directory by specified name mask.
// Preserve last N files by modified time.
// Return error only if can't read directory or delete file.
func ClearOldFiles(directory, filePrefix string, maxFiles int) error {
	dirContent := make(FileInfoSlice, 0, 16)
	dirContent, err := ioutil.ReadDir(directory)
	if err != nil {
		return err
	}
	if len(dirContent) <= maxFiles {
		return nil
	}
	validFiles := make(FileInfoSlice, 0, 16)
	rePrefix := regexp.MustCompile(filePrefix)
	for _, entity := range dirContent {
		if entity.IsDir() {
			continue
		}
		if !rePrefix.MatchString(entity.Name()) {
			continue
		}
		validFiles = append(validFiles, entity)
	}
	if len(validFiles) <= maxFiles {
		return nil
	}
	// Sort fined files.
	sort.Sort(validFiles)
	last := 0
	if len(validFiles) > maxFiles {
		last = len(validFiles) - maxFiles
	}
	for _, vf := range validFiles[:last] {
		fullPath := filepath.Join(directory, vf.Name())
		// Execute windows delete command
		winCommand := exec.Command("cmd", "/C", "del", fullPath)
		err = winCommand.Run()
		if err != nil {
			return err
		}
	}
	return nil
}

var ErrFolderContainNoFiles = fmt.Errorf("folder contains no files")

// Read previously saved registry key/value data from file.
// Automatically find latest .yaml file by name mask.
func ReadPreviouslySavedRegistryData(savedRegistryDirectory string) ([]RegistryValue, error) {
	// Read dir content.
	dirContent, err := ioutil.ReadDir(savedRegistryDirectory)
	if err != nil {
		return []RegistryValue{}, err
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
		} else {
		}
	}
	if lastRegFile == nil {
		return []RegistryValue{}, ErrFolderContainNoFiles
	}

	// Read data from file and unmarshal yaml.
	fullFilePath := filepath.Join(savedRegistryDirectory, lastRegFile.Name())
	regFile, err := os.Open(fullFilePath)
	if err != nil {
		return []RegistryValue{}, err
	}
	regBytes, err := ioutil.ReadAll(regFile)
	if err != nil {
		return []RegistryValue{}, err
	}
	registryData := make([]RegistryValue, 0, 32)
	err = yaml.Unmarshal(regBytes, &registryData)
	if err != nil {
		return []RegistryValue{}, err
	}
	return registryData, nil
}

// Save provided byte slice into provided by full path file.
// Automatically create directory and all needed parent directories.
func SaveBytesIntoFile(fullPath string, bytesData []byte) error {
	dirPath := filepath.Dir(fullPath)
	err := os.MkdirAll(dirPath, 0755)
	if err != nil {
		return err
	}
	registryFile, err := os.Create(fullPath)
	if err != nil {
		return err
	}
	defer registryFile.Close()
	_, err = registryFile.Write(bytesData)
	if err != nil {
		return err
	}
	return nil
}

// TODO - !!! test case where no registry directory
// TODO - change logic for mor reuse possibilities
// Save keys/value pairs from registry into file.
func SaveRegistryKeys(registryDir string) ([]byte, error) {
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
	registryBytes, err := yaml.Marshal(regValues)
	if err != nil {
		return nil, err
	}
	return registryBytes, nil
}

// Run executable file provided by full path and wait for it stop.
func RunAndWaitStop(fullPath string) error {
	cmd := exec.Command(fullPath)
	err := cmd.Start()
	if err != nil {
		return err
	}
	err = cmd.Wait()
	if err != nil {
		return err
	}
	return nil
}

// Extract configuration file and unmarshall collected data into config variable.
func ReadConfigFromYAMLFile() (MainCfgYAML, error) {
	log.Println("[START   ] ReadConfigFromYAMLFile")
	file, err := os.Open(confFile)
	if err != nil {
		log.Println("[FAIL    ] GetCustomisationFoldersList")
		return MainCfgYAML{}, err
	}
	data, err := ioutil.ReadAll(file)
	if err != nil {
		log.Println("[FAIL    ] GetCustomisationFoldersList")
		return MainCfgYAML{}, err
	}
	var mainConfig MainCfgYAML
	err = yaml.Unmarshal(data, &mainConfig)
	if err != nil {
		log.Println("[FAIL    ] GetCustomisationFoldersList")
		return MainCfgYAML{}, err
	}
	log.Println("[SUCCESS ] ReadConfigFromYAMLFile")
	return mainConfig, nil
}

// Get all folders in specified directory.
func GetCustomisationFoldersList(directory string) ([]string, error) {
	log.Println("[START   ] GetCustomisationFoldersList")
	entries, err := ioutil.ReadDir(directory)
	if err != nil {
		log.Println("[FAIL    ] GetCustomisationFoldersList")
		return nil, err
	}
	foldersList := make([]string, 0, 32)
	for _, entry := range entries {
		entryName := entry.Name()
		entryFullPath := filepath.Join(directory, entryName)
		fileInfo, err := os.Stat(entryFullPath)
		if err != nil {
			log.Println("[FAIL    ] GetCustomisationFoldersList")
			return nil, err
		}
		switch mode := fileInfo.Mode(); {
		case mode.IsDir():
			log.Printf("[     DIR] %s", entryName)
			foldersList = append(foldersList, entryName)
		default:
			log.Printf("[    FILE] %s", entryName)
		}
	}
	if len(foldersList) == 0 {
		log.Println("[FAIL    ] GetCustomisationFoldersList")
		return nil, errors.New(fmt.Sprint("Directory \"", directory, "\" does not contain subdirectories"))
	}

	log.Println("[SUCCESS ] GetCustomisationFoldersList")
	return foldersList, nil
}

// Collect customisation files from provided directory and all subfolders.
// For each fined file extract all possible CustomizationFile values.
func CollectCustomisationFiles(path, basePath string) ([]CustomizationFile, error) {
	collectedFiles := make([]CustomizationFile, 0, 16)
	err := filepath.Walk(path, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		extractedInfo, err := ExtractCustomFileInfo(info, path, basePath)
		if err != nil {
			return err
		}
		collectedFiles = append(collectedFiles, extractedInfo)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return collectedFiles, nil
}

// Extract all possible CustomizationFile values from provided file info
// and fill other data with default values.
func ExtractCustomFileInfo(fileInfo os.FileInfo, fullPath, basePath string) (CustomizationFile, error) {
	relativePath, err := filepath.Rel(basePath, fullPath)
	if err != nil {
		return CustomizationFile{}, err
	}
	relativePath = filepath.Dir(relativePath)
	if relativePath == "." {
		relativePath = ""
	}
	fileVersion, err := GetFileVersion(fullPath)
	return CustomizationFile{
		FileName:         fileInfo.Name(),
		RelativePath:     relativePath,
		DataFile:         "false",
		EntryPoint:       "false",
		IsMainConfigFile: "false",
		Optional:         "false",
		GroupName:        "",
		SourcePath:       fullPath,
		LastWriteTime:    fileInfo.ModTime(),
		Version:          fileVersion,
	}, nil
}

// Sort out all redundant files and older if present two or more files with equal FileName and RelativePath.
func ValidateCollectedFiles(list []CustomizationFile) ([]CustomizationFile, []string) {
	listLength := len(list)
	statuses := make([]string, listLength)
	resultList := make([]CustomizationFile, 0, listLength)
	redundancyRegexps := make([]*regexp.Regexp, 0, 16)
	redundancyRegexps = append(redundancyRegexps, regexp.MustCompile(`[Rr][Ee][Aa][Dd][Mm][Ee]`))
	redundancyRegexps = append(redundancyRegexps, regexp.MustCompile(`\.[Pp][Dd][Bb]$`))
	redundancyRegexps = append(redundancyRegexps, regexp.MustCompile(`\.[Mm][Dd]$`))
	for currentFileIndex, currentFile := range list {
		if statuses[currentFileIndex] != "" {
			continue
		}
		if CheckRedundancy(currentFile, redundancyRegexps) {
			statuses[currentFileIndex] = "[REDUNDANT]"
			continue
		}
		for compareFileIndex, compareFile := range list {
			if statuses[compareFileIndex] != "" {
				continue
			}
			if !(currentFile.FileName == compareFile.FileName && currentFile.RelativePath == compareFile.RelativePath) {
				continue
			}
			newFile := FindNewFile(currentFile, compareFile)
			if newFile == "second" {
				statuses[currentFileIndex] = "[SKIP     ]"
				currentFile = compareFile
				currentFileIndex = compareFileIndex
				continue
			}
			statuses[compareFileIndex] = "[SKIP     ]"
		}
		statuses[currentFileIndex] = "[COPIED   ]"
		resultList = append(resultList, currentFile)
	}
	return resultList, statuses
}

// Check provided file for redundancy by provided regexp rules.
func CheckRedundancy(file CustomizationFile, redundancyRegexps []*regexp.Regexp) bool {
	for _, re := range redundancyRegexps {
		if re.MatchString(file.FileName) {
			return true
		}
	}
	return false
}

// Compare two files and return which is newer.
func FindNewFile(first, second CustomizationFile) string {
	switch {
	case first.Version.full > second.Version.full:
		return "first"
	case first.Version.full < second.Version.full:
		return "second"
	case first.LastWriteTime.After(second.LastWriteTime):
		return "first"
	case first.LastWriteTime.Before(second.LastWriteTime):
		return "second"
	}
	return "equal"
}

// Write history file with provided data.
func WriteHistoryFile(
	fileList []CustomizationFile,
	customFilesFolder string,
	fileStatuses,
	customisationFolders []string,
	historyFileFullPath string,
	endChan chan bool,
) {
	defer DeferChannelSendTrue(endChan)
	historyFolder := filepath.Dir(historyFileFullPath)
	err := os.MkdirAll(historyFolder, 0755)
	if err != nil {
		log.Printf("WriteHistoryFile Error - '%+v'", err)
		return
	}
	historyFile, err := os.Create(historyFileFullPath)
	if err != nil {
		log.Printf("WriteHistoryFile Error - '%+v'", err)
		return
	}
	defer historyFile.Close()
	// Get current user name
	var currentUserName string
	CurrentUser, err := user.Current()
	if err != nil {
		log.Println("[ERROR] - Can't get current username")
		log.Println(err)
		currentUserName = "Can't resolve User Name"
	} else {
		if CurrentUser.Name == "" {
			currentUserName = CurrentUser.Username
		} else {
			currentUserName = CurrentUser.Name
		}
	}
	_, err = historyFile.WriteString(fmt.Sprint(
		"Program version: ",
		programVersion,
		"\n",
		"Started by: ",
		currentUserName,
		"\n\nCollected folders\n"))
	if err != nil {
		log.Printf("WriteHistoryFile Error - '%+v'", err)
		return
	}
	// Write found customisation folders
	for _, fName := range customisationFolders {
		_, err = historyFile.WriteString(fmt.Sprint(fName, "\n"))
		if err != nil {
			log.Printf("WriteHistoryFile Error - '%+v'", err)
			return
		}
	}
	// Write collected files statuses
	_, err = historyFile.WriteString("\nCollected files statuses\n")
	if err != nil {
		log.Printf("WriteHistoryFile Error - '%+v'", err)
		return
	}
	for index, file := range fileList {
		shortFilePath, err := filepath.Rel(customFilesFolder, file.SourcePath)
		if err != nil {
			log.Printf("WriteHistoryFile Error - '%+v'", err)
			return
		}
		fileStatusString := fmt.Sprint(fileStatuses[index], shortFilePath, "\n")
		_, err = historyFile.WriteString(fileStatusString)
		if err != nil {
			log.Printf("WriteHistoryFile Error - '%+v'", err)
			return
		}
	}
	err = ClearOldFiles(historyFolder, HistoryFileName, 15)
	if err != nil {
		log.Printf("can't clear old history files - '%v'", err)
	}
	return
}

// Wrapper for send data into channel from deffer.
func DeferChannelSendTrue(endChan chan bool) {
	endChan <- true
}

// TODO - replace CreateFolderIfNotExists with standard function
// Copy customisation files, from custom folder into WDE folder  with save relative path.
// Create subfolders if not exists.
func CopyCustomisationFiles(list []CustomizationFile, targetDirectory string) error {
	for _, file := range list {
		// Create subfolder if not exist
		if file.RelativePath != "" {
			err := CreateFolderIfNotExists(filepath.Join(targetDirectory, file.RelativePath))
			if err != nil && err != ErrFolderAlreadyExist {
				return err
			}
		}
		// Copy file
		targetFile := filepath.Join(targetDirectory, file.RelativePath, file.FileName)
		winCommand := exec.Command("cmd", "/C", "copy", "/Y", file.SourcePath, targetFile)
		err := winCommand.Run()
		if err != nil {
			return err
		}
	}
	return nil
}

// TODO - remove when replace CreateFolderIfNotExists with standard function
var ErrFolderAlreadyExist = fmt.Errorf("folder already exsist")

// TODO - replace with standard function
func CreateFolderIfNotExists(fullPath string) error {
	_, err := os.Stat(fullPath)
	if err != nil {
		if !os.IsNotExist(err) {
			return err
		}
		err = os.Mkdir(fullPath, 777)
		if err != nil {
			return err
		}
		return nil
	}
	return ErrFolderAlreadyExist
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

// Construct XML with format format valid for DM WDE.
func ConstructCustomFilesRegistryKey(customFilesList []CustomizationFile) string {
	result := RegFilesHeadXML
	for _, file := range customFilesList {
		result = fmt.Sprint(result, ConstructLineForCustomFilesRegistryKey(file))
	}
	return fmt.Sprint(result, RegFilesEndingXML)
}

// Convert variable of CustomizationFile type into string for registry key.
func ConstructLineForCustomFilesRegistryKey(cf CustomizationFile) string {
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

var ErrVersionNotExist = fmt.Errorf("version not exsist")

// Get file version from file info. Typcaly for .dll.
func GetFileVersion(path string) (FileVersion, error) {
	size := w32.GetFileVersionInfoSize(path)
	if size <= 0 {
		return FileVersion{}, ErrVersionNotExist
	}
	info := make([]byte, size)
	ok := w32.GetFileVersionInfo(path, info)
	if !ok {
		return FileVersion{}, ErrVersionNotExist
	}
	fixed, ok := w32.VerQueryValueRoot(info)
	if !ok {
		return FileVersion{}, ErrVersionNotExist
	}
	version := fixed.FileVersion()
	v1 := version & 0xFFFF000000000000 >> 48
	v2 := version & 0x0000FFFF00000000 >> 32
	v3 := version & 0x00000000FFFF0000 >> 16
	v4 := version & 0x000000000000FFFF >> 0
	log.Printf("file version: %d.%d.%d.%d\n", v1, v2, v3, v4)
	return FileVersion{version, v1, v2, v3, v4}, nil
}
