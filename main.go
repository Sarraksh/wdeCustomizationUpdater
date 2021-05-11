package main

import (
	"encoding/xml"
	"fmt"
	"go.uber.org/zap"
	"golang.org/x/sys/windows/registry"
	"io/ioutil"
	"log"
	"os"
	"os/exec"
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
)

// Struct for unmarshal XML from "CustomFiles" key
type XMLCustomFiles struct {
	XMLName         xml.Name            `xml:"ArrayOfApplicationFile"`
	ApplicationFile []CustomisationFile `xml:"ApplicationFile"`
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
	// Process logging options from config and apply default values if need.
	if mainConfig.Log.Folder != "" {
		logFullPath = mainConfig.Log.Folder
	} else {
		logFullPath = filepath.Join(programDirectory, "Log")
	}
	if mainConfig.Log.Name != "" {
		logName = fmt.Sprint(mainConfig.Log.Name, "_", startTimeString, ".log")
	} else {
		logName = fmt.Sprint("WdeCustomisationUpdater_", startTimeString, ".log")
	}
	logFullPath = filepath.Join(logFullPath, logName)
	logger := NewZapSimpleLoggerWithRotation(mainConfig.Log.Verbose, logFullPath, 10, 1)
	defer logger.Sync()

	// Get customisation folders list.
	logger.Info("Start collection customisation folders")
	foldersWithCustomisations, err := GetCustomisationFoldersList(mainConfig.CustomisationsFolder)
	if err != nil {
		logger.Error(fmt.Sprint("Customisation folders collection error - ", err))
		return
	}
	logger.Info("Customisation folders collected")

	// Get all files from  all customisation folders.
	logger.Info("Start collection customisation files")
	rowFilesList := make([]CustomisationFile, 0, 128)
	for _, folder := range foldersWithCustomisations {
		scanPath := filepath.Join(mainConfig.CustomisationsFolder, folder)
		tmpFilesList, err := CollectCustomisationFiles(scanPath, scanPath)
		if err != nil {
			logger.Error(fmt.Sprint("Customisation files collection error - ", err))
			return
		}
		rowFilesList = append(rowFilesList, tmpFilesList...)
	}
	logger.Info("Customisation files collected")

	// Filtering redundant and older files.
	// Get filtered files list and statuses of all original files.
	logger.Info("Start validation customisation files")
	finalFilesList, rowFilesStatuses := ValidateCollectedFiles(rowFilesList, mainConfig.RedundantFiles, logger)
	logger.Info("Customisation files validated")

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
		mainConfig.CustomisationsFolder,
		rowFilesStatuses,
		foldersWithCustomisations,
		historyFileFullPath,
		historyWritingEnd,
		logger,
	)

	// Copy all filtered files into WDE folder.
	logger.Info("Start copy validated customisation files into WDE folder")
	err = CopyCustomisationFiles(finalFilesList, filepath.Join(mainConfig.WDEInstallationFolder, WDESubfolder), logger)
	if err != nil {
		logger.Error(fmt.Sprint("Fail copy customisation files - ", err))
		return
	}
	logger.Info("Validated customisation files copied into WDE folder")

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

// Save provided byte slice into file provided by full path.
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
