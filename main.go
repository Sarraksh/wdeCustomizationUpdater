package main

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"github.com/gonutz/w32"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"golang.org/x/sys/windows/registry"
	"gopkg.in/natefinch/lumberjack.v2"
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
	programVersion   string = "2.0.2.0"                                   // Program version.
	confFile         string = "config.yaml"                               // Configuration file name.
	logHistLayout    string = "2006.01.02_150405"                         // Layout for "log" and "history" filenames time appending.
	WDESubfolder     string = "InteractionWorkspace"                      // WDE subfolder in MainCfgYAML.WDEInstallationFolder.
	DMSubfolder      string = "InteractionWorkspaceDeploymentManager"     // WDE Deployment Manager subfolder in MainCfgYAML.WDEInstallationFolder.
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
	WDEInstallationFolder string `yaml:"WDEInstallationFolder"`
	CustomizationsFolder  string `yaml:"CustomizationsFolder"`
	Log                   struct {
		Folder  string `yaml:"Folder"`
		Name    string `yaml:"Name"`
		Verbose string `yaml:"Verbose"`
	} `yaml:"Log"`
	RedundantFiles []string `yaml:"RedundantFiles"`
}

// Store file version in decimal.
type FileVersion struct {
	full uint64
	v1   uint64
	v2   uint64
	v3   uint64
	v4   uint64
}

// Store fined customization files with data needed for filtering, coping
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

func main() {
	// Fill program start information.
	startTime := time.Now()                            //Save start time.
	startTimeString := startTime.Format(logHistLayout) //Get string from startTime.
	programDirectory, _ := os.Getwd()                  //Save program folder.

	// Read configuration from file in working directory.
	// If fail, try get program directory from os.Args.
	mainConfig, err := ReadConfigFromYAMLFile(confFile)
	if err != nil {
		log.Printf("Can't read config file in current working directory `%v`", confFile)
		log.Println(err)
		log.Println("Try get program folder from arguments")
		programDirectory := filepath.Dir(os.Args[0])
		confFileAbsolutePath := filepath.Join(programDirectory, confFile)
		mainConfig, err = ReadConfigFromYAMLFile(confFileAbsolutePath)
		if err != nil {
			log.Printf("Can't read config file `%v`", confFileAbsolutePath)
			log.Println(err)
			log.Println("Program exited")
			return
		}
	}

	// Initialisation logging subsystem
	var logFullPath string
	var logName string
	if mainConfig.Log.Folder != "" {
		logFullPath = mainConfig.Log.Folder
	} else {
		logFullPath = filepath.Join(programDirectory, "Log")
	}
	if mainConfig.Log.Name != "" {
		logName = fmt.Sprint(mainConfig.Log.Name, "_", startTimeString, ".log")
	} else {
		logName = fmt.Sprint("WdeCustomizationUpdater_", startTimeString, ".log")
	}
	logFullPath = filepath.Join(logFullPath, logName)
	logger := NewZapSimpleLoggerWithRotation(mainConfig.Log.Verbose, logFullPath, 10, 1)
	defer logger.Sync()

	// Get customization folders list.
	logger.Info("Start collection customization folders")
	foldersWithCustomizations, err := GetCustomizationFoldersList(mainConfig.CustomizationsFolder)
	if err != nil {
		logger.Error(fmt.Sprint("Customization folders collection error - ", err))
		return
	}
	logger.Info("Customization folders collected")

	// Get all files from  all customization folders.
	logger.Info("Start collection customization files")
	rowFilesList := make([]CustomizationFile, 0, 128)
	for _, folder := range foldersWithCustomizations {
		scanPath := filepath.Join(mainConfig.CustomizationsFolder, folder)
		tmpFilesList, err := CollectCustomizationFiles(scanPath, scanPath)
		if err != nil {
			logger.Error(fmt.Sprint("Customization files collection error - ", err))
			return
		}
		rowFilesList = append(rowFilesList, tmpFilesList...)
	}
	logger.Info("Customization files collected")

	// Filtering redundant and older files.
	// Get filtered files list and statuses of all original files.
	logger.Info("Start validation customization files")
	finalFilesList, rowFilesStatuses := ValidateCollectedFiles(rowFilesList, mainConfig.RedundantFiles, logger)
	logger.Info("Customization files validated")

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
		foldersWithCustomizations,
		historyFileFullPath,
		historyWritingEnd,
		logger,
	)

	// Copy all filtered files into WDE folder.
	logger.Info("Start copy validated customization files into WDE folder")
	err = CopyCustomizationFiles(finalFilesList, filepath.Join(mainConfig.WDEInstallationFolder, WDESubfolder))
	if err != nil {
		logger.Error(fmt.Sprint("Fail copy customization files - ", err))
		return
	}
	logger.Info("Validated customization files copied into WDE folder")

	// Read previously saved registry data.
	// If there are no files to read, save the new registry data to a file and read from it.
	logger.Info("Prepare registry data")
	savedRegistryDir := filepath.Join(programDirectory, SavedRegFolder)
	var regData RegistryValues
	var RegDataByte []byte
	logger.Info("Reading previously saved registry data")
	err = os.MkdirAll(savedRegistryDir, 0755)
	if err != nil {
		logger.Warn(fmt.Sprint("Can't create folder for previously saved registry - ", err))
		return
	}
	RegDataByte, err = ReadPreviouslySavedRegistryData(savedRegistryDir)
	if err != nil {
		if err != ErrNoFilesFoundInFolderByPattern {
			logger.Error(fmt.Sprint("Reading previously saved registry data from file failed - ", err))
			return
		}
		logger.Info("No previously registry data saved. Try read from current user registry data")
		regData, err = ReadRegistryData(DMRegistryDir)
		switch err {
		case nil:
			logger.Info("Save current user registry data as initialisation data")
		case registry.ErrNotExist:
			logger.Info("No data in current user registry. Save zeroed initialisation data")
			regData = make([]RegistryValue, 0, 32)
		default:
			logger.Error(fmt.Sprint("Reading current user registry data error - ", err))
			return
		}
		registryFileFullPath := filepath.Join(
			programDirectory,
			SavedRegFolder,
			fmt.Sprint(RegFileName, "INITIALISATION_", startTimeString, ".yaml"),
		)
		logger.Info("Marshal collected registry data")
		RegDataByte, err = MarshalRegistryData(regData)
		if err != nil {
			logger.Error(fmt.Sprint("Can't marshal registry data into YAML - ", err))
			return
		}
		logger.Info("Save Marshaled registry data into file")
		err = SaveBytesIntoFile(registryFileFullPath, RegDataByte)
		if err != nil {
			logger.Error(fmt.Sprint("Can't save registry data into file - ", err))
			return
		}
		logger.Info("Initialisation registry data saved")
	} else {
		logger.Info("Unmarshal previously saved registry data")
		regData, err = UnmarshalRegistryData(RegDataByte)
		if err != nil {
			logger.Error(fmt.Sprint("Can't unmarshal registry data from YAML - ", err))
			return
		}
	}
	logger.Info("Registry data prepared")

	// Update data previously saved from registry and now read from file.
	logger.Info("Update old registry data with new data")
	regData.InsertAddCustomFileTrueValue()                // Force set "AddCustomFile" with "True"
	err = regData.AddManuallyAddedOptions(finalFilesList) // Combine manually added options and new collected files.
	if err != nil {
		if err == ErrCustomFilesNotFound {
			logger.Info("Old registry data contain not \"CustomFiles\" key. Add fully new data for \"CustomFiles\" key")
			regData.InsertActualCustomFilesValue(ConstructCustomFilesRegistryKey(finalFilesList))
		} else {
			logger.Error(fmt.Sprint("Can't update old registry data with new data - ", err))
		}
	}

	// Write prepared data into registry.
	logger.Info("Start writing prepared data into registry")
	err = WriteToRegistry(regData)
	if err != nil {
		logger.Error(fmt.Sprint("Can't write into registry - ", err))
		return
	}
	logger.Info("Write into registry successful")

	// Run WDE Deployment Manager and wait while it stop.
	logger.Info("Run WDE Deployment Manager")
	err = RunAndWaitStop(filepath.Join(mainConfig.WDEInstallationFolder, DMSubfolder), DMExecutableName, logger)
	if err != nil {
		logger.Error(fmt.Sprint("WDE deployment manager error - ", err))
		return
	}

	logger.Info("WDE Deployment Manager stopped")

	// Save actual registry data into file.
	logger.Info("Save actual registry data into file")
	regData, err = ReadRegistryData(DMRegistryDir)
	if err != nil {
		logger.Error(fmt.Sprint("Can't save registry data after WDE Deployment Manager - ", err))
		return
	}
	registryBytes, err := MarshalRegistryData(regData)
	if err != nil {
		logger.Error(fmt.Sprint("Can't marshal registry data into YAML - ", err))
		return
	}
	registryFileFullPath := filepath.Join(
		programDirectory,
		SavedRegFolder,
		fmt.Sprint(RegFileName, startTimeString, ".yaml"),
	)
	err = SaveBytesIntoFile(registryFileFullPath, registryBytes)
	if err != nil {
		logger.Error(fmt.Sprint("Can't save registry data into file - ", err))
		return
	}
	logger.Info("Write data into file successful")

	// Clean old registry files. Preserve last 5 files for backup purposes.
	logger.Info("Delete old registry files")
	err = ClearOldFiles(filepath.Join(programDirectory, SavedRegFolder), RegFileName, 15)
	if err != nil {
		logger.Error(fmt.Sprint("Can't delete old registry files - ", err))
	}
	logger.Info("Delete old log files")
	err = ClearOldFiles(filepath.Join(programDirectory, SavedRegFolder), RegFileName, 15)
	if err != nil {
		logger.Error(fmt.Sprint("Can't delete old log files - ", err))
	}
	logger.Info("Old files cleared")

	// Wait for the history file to finish writing end exit program.
	logger.Info(fmt.Sprintf("History writing stopped '%v'", <-historyWritingEnd))
	logger.Info("WDE customisation updated successful.")
}

// Extract configuration file and unmarshall collected data into config variable.
func ReadConfigFromYAMLFile(cfgFilePath string) (MainCfgYAML, error) {
	log.Println("[START   ] ReadConfigFromYAMLFile")
	file, err := os.Open(cfgFilePath)
	if err != nil {
		log.Println("[FAIL    ] GetCustomizationFoldersList")
		return MainCfgYAML{}, err
	}
	data, err := ioutil.ReadAll(file)
	if err != nil {
		log.Println("[FAIL    ] GetCustomizationFoldersList")
		return MainCfgYAML{}, err
	}
	var mainConfig MainCfgYAML
	err = yaml.Unmarshal(data, &mainConfig)
	if err != nil {
		log.Println("[FAIL    ] GetCustomizationFoldersList")
		return MainCfgYAML{}, err
	}
	log.Println("[SUCCESS ] ReadConfigFromYAMLFile")
	return mainConfig, nil
}

// Return simple logger with rotation. v1.
// Take logging level, full path to log file, vax size of log file in MB and number of backup files.
// Have no time limit for store log files
func NewZapSimpleLoggerWithRotation(logLevelStr string, logFilePath string, maxSize, maxBackups int) *zap.Logger {
	var logLevel zapcore.Level
	err := logLevel.UnmarshalText([]byte(logLevelStr))
	if err != nil {
		logLevel = zapcore.ErrorLevel
	}

	var cfg zap.Config
	cfg.EncoderConfig.TimeKey = "time"
	cfg.EncoderConfig.MessageKey = "message"
	cfg.EncoderConfig.LevelKey = "level"
	cfg.EncoderConfig.EncodeTime = zapcore.TimeEncoderOfLayout("2006.01.02 15:04:05")
	cfg.EncoderConfig.EncodeLevel = zapcore.CapitalLevelEncoder

	writer := zapcore.AddSync(&lumberjack.Logger{
		Filename:   logFilePath,
		MaxSize:    maxSize, // megabytes
		MaxBackups: maxBackups,
	})

	core := zapcore.NewCore(
		zapcore.NewConsoleEncoder(cfg.EncoderConfig),
		writer,
		logLevel,
	)
	logger := zap.New(core)

	return logger
}

// Get all folders in specified directory.
func GetCustomizationFoldersList(directory string) ([]string, error) {
	entries, err := ioutil.ReadDir(directory)
	if err != nil {
		return nil, err
	}
	foldersList := make([]string, 0, 32)
	for _, entry := range entries {
		entryName := entry.Name()
		entryFullPath := filepath.Join(directory, entryName)
		fileInfo, err := os.Stat(entryFullPath)
		if err != nil {
			return nil, err
		}
		switch mode := fileInfo.Mode(); {
		case mode.IsDir():
			foldersList = append(foldersList, entryName)
		default:
		}
	}
	if len(foldersList) == 0 {
		return nil, errors.New(fmt.Sprint("Directory \"", directory, "\" does not contain subdirectories"))
	}
	return foldersList, nil
}

// Collect customization files from provided directory and all subfolders.
// For each fined file extract all possible CustomizationFile values.
func CollectCustomizationFiles(path, basePath string) ([]CustomizationFile, error) {
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

var ErrVersionNotExist = fmt.Errorf("version not exsist")

// Get file version from file info. Typically for .dll.
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
	return FileVersion{version, v1, v2, v3, v4}, nil
}

// Sort out all redundant files and older if present two or more files with equal FileName and RelativePath.
func ValidateCollectedFiles(list []CustomizationFile, redundantCFG []string, logger *zap.Logger) ([]CustomizationFile, []string) {
	listLength := len(list)
	statuses := make([]string, listLength)
	resultList := make([]CustomizationFile, 0, listLength)
	redundancyRegexps := make([]*regexp.Regexp, 0, 16)

	// Convert redundant files patterns from config for handle case sensitivity and match only file extensions if leading by dot.
	for _, rf := range redundantCFG {
		if string(rf[0]) == "." {
			rf = fmt.Sprint(rf, "$")
		}
		rf = fmt.Sprint("(?i)", rf)
		logger.Debug(fmt.Sprintf("redundant regexp result    - '%+v'", rf))
		redundancyRegexps = append(redundancyRegexps, regexp.MustCompile(rf))
	}

	// Hardcode basic redundant file patterns to avoid human factor
	logger.Debug(fmt.Sprintf("redundant regexp mandatory - '%+v'", `(?i)readme`))
	logger.Debug(fmt.Sprintf("redundant regexp mandatory - '%+v'", `(?i)\.pdb$`))
	logger.Debug(fmt.Sprintf("redundant regexp mandatory - '%+v'", `(?i)\.md$`))
	redundancyRegexps = append(redundancyRegexps, regexp.MustCompile(`(?i)readme`))
	redundancyRegexps = append(redundancyRegexps, regexp.MustCompile(`(?i)\.pdb$`))
	redundancyRegexps = append(redundancyRegexps, regexp.MustCompile(`(?i)\.md$`))

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
	customizationFolders []string,
	historyFileFullPath string,
	endChan chan bool,
	logger *zap.Logger,
) {
	defer DeferChannelSendTrue(endChan)
	logger.Info("(WriteHistoryFile) Start writing to history file")
	historyFolder := filepath.Dir(historyFileFullPath)
	err := os.MkdirAll(historyFolder, 0755)
	if err != nil {
		logger.Warn(fmt.Sprint("(WriteHistoryFile) History file not written - ", err))
		return
	}
	historyFile, err := os.Create(historyFileFullPath)
	if err != nil {
		logger.Warn(fmt.Sprint("(WriteHistoryFile) History file not written - ", err))
		return
	}
	defer historyFile.Close()

	// Get current user name
	var currentUserName string
	CurrentUser, err := user.Current()
	if err != nil {
		logger.Warn(fmt.Sprint("(WriteHistoryFile) Can't get current user name while save history- ", err))
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
		logger.Warn(fmt.Sprint("(WriteHistoryFile) History file not written - ", err))
		return
	}
	// Write found customization folders
	for _, fName := range customizationFolders {
		_, err = historyFile.WriteString(fmt.Sprint(fName, "\n"))
		if err != nil {
			logger.Warn(fmt.Sprint("(WriteHistoryFile) History file not written - ", err))
			return
		}
	}
	// Write collected files statuses
	_, err = historyFile.WriteString("\nCollected files statuses\n")
	if err != nil {
		logger.Warn(fmt.Sprint("(WriteHistoryFile) History file not written - ", err))
		return
	}
	for index, file := range fileList {
		shortFilePath, err := filepath.Rel(customFilesFolder, file.SourcePath)
		if err != nil {
			logger.Warn(fmt.Sprint("(WriteHistoryFile) History file not written - ", err))
			return
		}
		fileStatusString := fmt.Sprint(fileStatuses[index], shortFilePath, "\n")
		_, err = historyFile.WriteString(fileStatusString)
		if err != nil {
			logger.Warn(fmt.Sprint("(WriteHistoryFile) History file not written - ", err))
			return
		}
	}
	logger.Info("(WriteHistoryFile) History file written successfully")
	err = ClearOldFiles(historyFolder, HistoryFileName, 15)
	if err != nil {
		logger.Warn(fmt.Sprint("(WriteHistoryFile) Can't clear old history files - ", err))
	}
	return
}

// Wrapper for send data into channel from deffer.
func DeferChannelSendTrue(endChan chan bool) {
	endChan <- true
}

// Copy customization files, from custom folder into WDE folder  with save relative path.
// Create subfolders if not exists.
func CopyCustomizationFiles(list []CustomizationFile, targetDirectory string) error {
	for _, file := range list {
		// Create subfolder if not exist
		if file.RelativePath != "" {
			err := os.MkdirAll(filepath.Join(targetDirectory, file.RelativePath), 0755)
			if err != nil {
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

var ErrNoFilesFoundInFolderByPattern = fmt.Errorf("folder contains no files")

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

// Unmarshal XML from string and return CustomizationFile slice with filled
// FileName, RelativePath, DataFile, EntryPoint, IsMainConfigFile, Optional and GroupName values.
func ParseOldCustomFilesValue(oldCustomFiles []byte) ([]CustomizationFile, error) {
	var oldData XMLCustomFiles
	decoderXML := xml.NewDecoder(bytes.NewReader(oldCustomFiles))
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

// Construct XML with format valid for DM WDE.
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

// Clear files in specified directory by specified name mask.
// Preserve last N files by modified time.
// Return error if can't read directory or delete file.
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

// Run executable file provided by full path and wait for it stop.
func RunAndWaitStop(directory, fileName string, logger *zap.Logger) error {
	fileName = fmt.Sprint("./", fileName)
	cmd := exec.Command(fileName)
	cmd.Dir = directory
	logger.Debug(fmt.Sprintf("Run file '%+v' from dir '%+v'", fileName, directory))
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
