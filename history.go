package main

import (
	"fmt"
	"go.uber.org/zap"
	"os"
	"os/user"
	"path/filepath"
)

// Write history file with provided data.
func WriteHistoryFile(
	fileList []CustomisationFile,
	customFilesFolder string,
	fileStatuses,
	customisationFolders []string,
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
	// Write found customisation folders
	for _, fName := range customisationFolders {
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
