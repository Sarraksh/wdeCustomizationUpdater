package main

import (
	"errors"
	"fmt"
	"github.com/gonutz/w32"
	"go.uber.org/zap"
	"io"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"time"
)

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
type CustomisationFile struct {
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

// Get all folders in specified directory.
func GetCustomisationFoldersList(directory string) ([]string, error) {
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

// Collect customisation files from provided directory and all subfolders.
// For each fined file extract all possible CustomisationFile values.
func CollectCustomisationFiles(path, basePath string) ([]CustomisationFile, error) {
	collectedFiles := make([]CustomisationFile, 0, 16)
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

// Extract all possible CustomisationFile values from provided file info
// and fill other data with default values.
func ExtractCustomFileInfo(fileInfo os.FileInfo, fullPath, basePath string) (CustomisationFile, error) {
	relativePath, err := filepath.Rel(basePath, fullPath)
	if err != nil {
		return CustomisationFile{}, err
	}
	relativePath = filepath.Dir(relativePath)
	if relativePath == "." {
		relativePath = ""
	}
	fileVersion, err := GetFileVersion(fullPath)
	return CustomisationFile{
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
func ValidateCollectedFiles(list []CustomisationFile, redundantCFG []string, logger *zap.Logger) ([]CustomisationFile, []string) {
	listLength := len(list)
	statuses := make([]string, listLength)
	resultList := make([]CustomisationFile, 0, listLength)
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
func CheckRedundancy(file CustomisationFile, redundancyRegexps []*regexp.Regexp) bool {
	for _, re := range redundancyRegexps {
		if re.MatchString(file.FileName) {
			return true
		}
	}
	return false
}

// Compare two files and return which is newer.
func FindNewFile(first, second CustomisationFile) string {
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

// Copy customisation files, from custom folder into WDE folder  with save relative path.
// Create subfolders if not exists.
func CopyCustomisationFiles(list []CustomisationFile, targetDirectory string, logger *zap.Logger) error {
	for _, file := range list {
		logger.Debug(fmt.Sprintf("Start file '%+v'", file))
		// Create subfolder if not exist
		if file.RelativePath != "" {
			err := os.MkdirAll(filepath.Join(targetDirectory, file.RelativePath), 0755)
			if err != nil {
				logger.Error(fmt.Sprintf("While create folder '%+v'", filepath.Join(targetDirectory, file.RelativePath)))
				return err
			}
		}

		// TODO - remove cmd copy or make it alternative
		// Copy file with cmd command.
		// If copy failed use builtin copy method.
		targetFile := filepath.Join(targetDirectory, file.RelativePath, file.FileName)
		winCommand := exec.Command("cmd", "/C", "copy", "/Y", file.SourcePath, targetFile)
		err := winCommand.Run()
		if err != nil {
			logger.Error(fmt.Sprintf("While copy file '%+v' with command '%+v'", targetFile, winCommand))
			logger.Error("Try another method")
			_, err := copyFile(file.SourcePath, targetFile)
			if err != nil {
				logger.Error("Another method failed")
				return err
			}
		}
	}
	return nil
}

// Builtin copy method.
func copyFile(src, dst string) (int64, error) {
	sourceFileStat, err := os.Stat(src)
	if err != nil {
		return 0, err
	}

	if !sourceFileStat.Mode().IsRegular() {
		return 0, fmt.Errorf("%s is not a regular file", src)
	}

	source, err := os.Open(src)
	if err != nil {
		return 0, err
	}
	defer source.Close()

	destination, err := os.Create(dst)
	if err != nil {
		return 0, err
	}
	defer destination.Close()
	nBytes, err := io.Copy(destination, source)
	return nBytes, err
}
