package main

import (
	"bufio"
	"github.com/gonutz/w32"
	"golang.org/x/sys/windows/registry"
	"io/ioutil"
	"log"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"time"
)

const (
	programVersion string = "1.1.3.0"              //Program version
	confFile       string = "application.cfg"      //Configuration file name
	logHistLayout  string = "2006.01.02_150405"    //Layout for "log" and "history" filenames time appending
	logBreakString        = "\n===\n\n\n\n\n===\n" //String for visual break in log file

	//Initialization of the constants for fill up "CustomFiles" registry key
	AppFile1 = "  <ApplicationFile FileName=\""
	AppFile2 = "\" RelativePath=\""
	AppFile3 = "\" DataFile=\"false\" EntryPoint=\"false\" IsMainConfigFile=\"false\" Optional=\"false\" GroupName=\"\" />\n"
)

//Store file version in digits
type FileVersion struct {
	v1 uint16
	v2 uint16
	v3 uint16
	v4 uint16
}

//provide error handling for closing file in deffer
func closeFile(fileForClose *os.File) {
	err := fileForClose.Close()
	if err != nil {
		log.Println(err)
	}
}

var CollectedFileVersions = make(map[string]FileVersion)
var NilVersion = FileVersion{0, 0, 0, 0}

func main() {

	startTime := time.Now()                            //Save start time
	startTimeString := startTime.Format(logHistLayout) //Get string from startTime
	programDirectory, _ := os.Getwd()                  //Save program folder

	//Generate log file name
	logFilePath := "WDECustoms_LOG_" + startTimeString + ".txt"
	logFilePath = filepath.Join(programDirectory, "log", logFilePath)
	logFolderPath := filepath.Join(programDirectory, "log")
	//Create "log" folder if it not exists
	if _, err := os.Stat(logFolderPath); os.IsNotExist(err) {
		log.Printf("[DEBUG] - log folder does not exists. Creating - %v\n", logFolderPath)
		err := os.Mkdir(logFolderPath, 777)
		if err != nil {
			log.Printf("[ERROR] - creating log folder - %v", err)
		}
	}

	//Create a log file and redirect output to it, if possible
	logFile, err := os.OpenFile(logFilePath, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0666)
	if err != nil {
		log.Fatalf("error opening file: %v", err)
	} else {
		log.SetOutput(logFile)
		log.Println("Program started")
		log.Println("Version: ", programVersion)
		defer closeFile(logFile)
	}

	//Generate history file name
	historyFilePath := "WDECustoms_History_" + startTimeString + ".txt"
	historyFilePath = filepath.Join(programDirectory, "history", historyFilePath)
	historyFolderPath := filepath.Join(programDirectory, "history")
	//Create "history" folder if it not exists
	if _, err := os.Stat(historyFolderPath); os.IsNotExist(err) {
		log.Printf("[DEBUG] - history folder does not exists. Creating - %v\n", historyFolderPath)
		err := os.Mkdir(historyFolderPath, 777)
		if err != nil {
			log.Printf("[ERROR] - creating history folder - %v", err)
		}
	}
	//Create a history file
	historyFile, err := os.Create(historyFilePath)
	if err != nil {
		log.Println(err)
	} else {
		log.Println("History file created - ", historyFilePath)
	}

	//Get current user name
	CustomUser, err := user.Current()
	if err != nil {
		log.Panic(err)
	} else {
		log.Println("Current user name - ", CustomUser)
	}

	//Write the name of the user who launched the program to the history file
	_, err = historyFile.WriteString("Program version" + programVersion + "\n" + "Started by: " + CustomUser.Name + "\n")
	if err != nil {
		log.Println(err)
		err := historyFile.Close()
		if err != nil {
			log.Printf("[ERROR] - closing history file - %v", err)
		}
	}

	//Checking for the existence of a conf file and terminating the program if it does not exist
	if _, err := os.Stat(confFile); os.IsNotExist(err) {
		log.Printf("[ERROR] - can't find %q", confFile)
		return
	}

	//Opening the conf file to read and exit the program, if failed
	confFileContent, err := os.Open(confFile)
	if err != nil {
		log.Fatalf("[ERROR] - failed opening file: %s", err)
		return
	} else {
		log.Println("[DEBUG] - Start reading configuration file")
	}

	//Read conf file
	scanner := bufio.NewScanner(confFileContent)
	scanner.Split(bufio.ScanLines)

	rePath := regexp.MustCompile(`".*"`) //Regexp to get contains folder options

	//CustomizationsFolder
	customsPathPresence := regexp.MustCompile(`^CustomizationsFolder = `)
	var customsPath string

	//WDEFolder
	WDEPathPresence := regexp.MustCompile(`^WDEFolder = `)
	var WDEPath string

	//Get options from file and validate them
	var tmpString string
	for scanner.Scan() {
		tmpString = scanner.Text()
		//Get CustomizationsFolder option
		if customsPathPresence.MatchString(tmpString) {
			log.Println("[DEBUG] - CustomizationsFolder option presence")
			customsPath = rePath.FindString(tmpString)
			customsPath = customsPath[1 : len(customsPath)-1]
			if filepath.IsAbs(customsPath) {
				log.Printf("[DEBUG] - CustomizationsFolder option valid\t- %s", customsPath)
			} else {
				log.Println("[ERROR] - CustomizationsFolder option DOES NOT valid")
				return
			}
		}
		//Get WDEFolder option
		if WDEPathPresence.MatchString(tmpString) {
			log.Println("[DEBUG] - WDEFolder option presence")
			WDEPath = rePath.FindString(tmpString)
			WDEPath = WDEPath[1 : len(WDEPath)-1]
			if filepath.IsAbs(WDEPath) {
				log.Printf("[DEBUG] - WDEPath option valid\t- %s", WDEPath)
			} else {
				log.Println("[ERROR] - WDEPath option DOES NOT valid")
				return
			}
		}
	}

	err = confFileContent.Close() //Close conf file
	if err != nil {
		log.Printf("[ERROR] - closing configuration file - %v", err)
	}
	log.Println("[DEBUG] - Stop reading configuration file")

	//Checking the presence of required parameters
	if customsPath == "" {
		log.Println("[ERROR] - CustomizationsFolder option DOES NOT presence")
		return
	}
	if WDEPath == "" {
		log.Println("[ERROR] - WDEFolder option DOES NOT presence")
		return
	}

	log.Printf(logBreakString) //Visual break in the log

	//Get folders list in CustomizationsFolder
	customsFoldersList, err := GetCustomFoldersList(customsPath)
	if _, err := os.Stat(confFile); os.IsNotExist(err) {
		log.Printf("[ERROR] - in GetCustomFoldersList")
		return
	}

	//Put list of finded folder into history file
	log.Println("[DEBUG] - Put list of finded folder into history file")
	_, err = historyFile.WriteString("=============Finded folders=============\n")
	if err != nil {
		log.Println(err)
		log.Println("[ERROR] - Error writing history. No further history will be recorded.")
		err = historyFile.Close()
		if err != nil {
			log.Printf("[ERROR] - closing history file - %v", err)
		}
	}
	for _, s := range customsFoldersList {
		log.Println(s)
		_, err = historyFile.WriteString(s + "\n")
		if err != nil {
			log.Println(err)
			log.Println("[ERROR] - Error writing history. No further history will be recorded.")
			err = historyFile.Close()
			if err != nil {
				log.Printf("[ERROR] - closing history file - %v", err)
			}
		}
	}

	//Initialization of the variable for the "CustomFiles" registry key value
	registryCustomFiles := "<?xml version=\"1.0\" encoding=\"utf-16\"?>\n<ArrayOfApplicationFile xmlns:xsi=\"http://www.w3.org/2001/XMLSchema-instance\" xmlns:xsd=\"http://www.w3.org/2001/XMLSchema\">\n"

	//Put list of copyed files into history file
	_, err = historyFile.WriteString("=============Copyed Files=============\n")
	if err != nil {
		log.Println(err)
		log.Println("[ERROR] - Error writing history. No further history will be recorded.")
		err = historyFile.Close()
		if err != nil {
			log.Printf("[ERROR] - closing history file - %v", err)
		}
	}

	log.Printf("[DEBUG] - Customizations folders counted - %d", len(customsFoldersList))
	filesList := make(map[string]time.Time, len(customsFoldersList)*5) //make map for customisation files
	log.Printf("[DEBUG] - Customizations files expecting - %d", len(customsFoldersList)*5)

	//Prepare regexps

	//to skip redundant files
	reReadme := regexp.MustCompile(`[Rr][Ee][Aa][Dd][Mm][Ee]`)
	rePDB := regexp.MustCompile(`\.[Pp][Dd][Bb]$`)
	//to handle files in subfulders
	reIsFilePath := regexp.MustCompile(`\\`)
	reGetSubfolder := regexp.MustCompile(`^[0-9A-Za-z: _]*`)

	skipReason := ""
	var CurrentFileVersion FileVersion

	//Analysis of the contents of the finded customization folders.
	for _, cFolder := range customsFoldersList {
		_, err = historyFile.WriteString("---------------" + cFolder + "\n")
		if err != nil {
			log.Println(err)
			log.Println("[ERROR] - Error writing history. No further history will be recorded.")
			err = historyFile.Close()
			if err != nil {
				log.Printf("[ERROR] - closing history file - %v", err)
			}
		}

		//Write into log file current customization folder
		log.Printf("----------%v\n", cFolder)
		cFolderFull := filepath.Join(customsPath, cFolder)
		log.Printf("----------%v\n", cFolderFull)

		err = os.Chdir(cFolderFull) //Change working directory to current customization folder
		if err != nil {
			log.Printf("[ERROR] - Change working directory to current customization folder - %v", err)
		}

		//Walk thru current customization folder for files
		err = filepath.Walk(".", func(path string, info os.FileInfo, err error) error {
			if err != nil {
				log.Println(err)
				log.Printf("[ERROR] - in filepath.Walk on %q\n", path)
				return err
			}

			//Retrieving item information
			fi, err := os.Stat(path)
			if err != nil {
				log.Println(err)
				log.Printf("[ERROR] - Error retrieving item information - %s", path)
				return err
			}

			//Object type definition
			switch mode := fi.Mode(); {
			//Perform an action on a folder
			case mode.IsDir():
				tmpP := filepath.Join(WDEPath, path)
				log.Printf("[DEBUG] - Find Folder (skip)\t%s\n", tmpP)
			//Perform an action on a file
			case mode.IsRegular():
				//prepare and log some variables
				log.Printf("[DEBUG] - Copy file      (Source)\t%s\n", path)
				tmpP := filepath.Join(cFolderFull, path)
				log.Printf("[DEBUG] - Copy file (Source Full)\t%s\n", tmpP)
				out := filepath.Join(WDEPath, path)
				log.Printf("[DEBUG] - Copy file (Destination)\t%s\n", out)

				//Check files to skip redundant
				if reReadme.MatchString(path) {
					skipReason = "Readme"
				}
				if rePDB.MatchString(path) {
					skipReason = ".PDB"
				}

				if skipReason != "" {
					_, err = historyFile.WriteString("REDUNDANT - " + skipReason + " - " + path + "\n")
					if err != nil {
						log.Println(err)
						log.Println("[ERROR] - Error writing history. No further history will be recorded.")
						err = historyFile.Close()
						if err != nil {
							log.Printf("[ERROR] - closing history file - %v", err)
						}
					}
					log.Printf("[DEBUG] - Not copied  (REDUNDANT)\t%s\n", out)
					skipReason = ""
				} else {

					CurrentFileVersion = GetFileVersion(tmpP)
					//Check if file already copied
					if _, ok := filesList[out]; ok {
						//Check modification time and replace if newer
						log.Printf("[DEBUG] - Previous file name      - %v", out)
						log.Printf("[DEBUG] - Previous file mod time  - %v", filesList[out])
						log.Printf("[DEBUG] - Next file     name      - %v", fi.Name())
						log.Printf("[DEBUG] - Next file     mod time  - %v", fi.ModTime())
						if filesList[out].Before(fi.ModTime()) {
							if IsFileVersionGrater(CurrentFileVersion, CollectedFileVersions[out]) {
								log.Printf("[DEBUG] - File replaced with a newer file (REPLACE)\t%s\n", out)
								CollectedFileVersions[out] = CurrentFileVersion
								//exec OS copy command
								cmd := exec.Command("cmd", "/C", "copy", "/Y", tmpP, out)
								err = cmd.Run()
								if err != nil {
									log.Printf("[ERROR] - Error copy file from %v to %v\n", tmpP, out)
									log.Fatal(err)
								} else {
									_, err = historyFile.WriteString("REPLACE - File replaced with a newer file - " + path + "\n")
									if err != nil {
										log.Println(err)
										log.Println("[ERROR] - Error writing history. No further history will be recorded.")
										err = historyFile.Close()
										if err != nil {
											log.Printf("[ERROR] - closing history file - %v", err)
										}
									}
									filesList[out] = fi.ModTime()
								}
							} else {
								log.Printf("[DEBUG] - FIle not copied  (older or equal version)\t%s\n", out)
								_, err = historyFile.WriteString("SKIP - Newer or same version of file already copied - " + path + "\n")
								if err != nil {
									log.Println(err)
									log.Println("[ERROR] - Error writing history. No further history will be recorded.")
									err = historyFile.Close()
									if err != nil {
										log.Printf("[ERROR] - closing history file - %v", err)
									}
								}
							}
						} else {
							if CurrentFileVersion == NilVersion {
								log.Printf("[DEBUG] - FIle not copied  (NOPE)\t%s\n", out)
								_, err = historyFile.WriteString("SKIP - Newer or same file already copied - " + path + "\n")
								if err != nil {
									log.Println(err)
									log.Println("[ERROR] - Error writing history. No further history will be recorded.")
									err = historyFile.Close()
									if err != nil {
										log.Printf("[ERROR] - closing history file - %v", err)
									}
								}
							} else {
								if IsFileVersionGrater(CurrentFileVersion, CollectedFileVersions[out]) {
									log.Printf("[DEBUG] - File replaced with a newer version (REPLACE)\t%s\n", out)
									CollectedFileVersions[out] = CurrentFileVersion
									//exec OS copy command
									cmd := exec.Command("cmd", "/C", "copy", "/Y", tmpP, out)
									err = cmd.Run()
									if err != nil {
										log.Printf("[ERROR] - Error copy file from %v to %v\n", tmpP, out)
										log.Fatal(err)
									} else {
										_, err = historyFile.WriteString("REPLACE - File replaced with a newer version - " + path + "\n")
										if err != nil {
											log.Println(err)
											log.Println("[ERROR] - Error writing history. No further history will be recorded.")
											err = historyFile.Close()
											if err != nil {
												log.Printf("[ERROR] - closing history file - %v", err)
											}
										}
										filesList[out] = fi.ModTime()
									}
								} else {
									log.Printf("[DEBUG] - FIle not copied  (older or equal version)\t%s\n", out)
									_, err = historyFile.WriteString("SKIP - Newer or same version of file already copied - " + path + "\n")
									if err != nil {
										log.Println(err)
										log.Println("[ERROR] - Error writing history. No further history will be recorded.")
										err = historyFile.Close()
										if err != nil {
											log.Printf("[ERROR] - closing history file - %v", err)
										}
									}
								}
							}
						}
					} else {
						filesList[out] = fi.ModTime()
						if CurrentFileVersion != NilVersion {
							CollectedFileVersions[out] = CurrentFileVersion
						}
						//check if file mast be plased in subfolder or in root and feel registry key
						if reIsFilePath.MatchString(path) {
							registryCustomFiles = registryCustomFiles + AppFile1 + fi.Name() + AppFile2 + reGetSubfolder.FindString(path) + AppFile3
							log.Printf("[INFO ] - Copy file   (SUBFOLDER)\t%s\n", reGetSubfolder.FindString(path))

							//check subfolder existing and if not create it
							outDir := filepath.Dir(out)
							if _, err := os.Stat(outDir); os.IsNotExist(err) {
								log.Printf("[DEBUG] - subfolder does not exists. Creating - %v\n", logFolderPath)
								err = os.Mkdir(outDir, 777)
								if err != nil {
									log.Printf("[ERROR] - creating log folder - %v", err)
								}
							}
						} else {
							log.Printf("[INFO ] - Copy file        (ROOT)\t%s\n", fi.Name())
							registryCustomFiles = registryCustomFiles + AppFile1 + fi.Name() + AppFile2 + AppFile3
						}

						//exec OS copy comand
						cmd := exec.Command("cmd", "/C", "copy", "/Y", tmpP, out)
						err = cmd.Run()
						if err != nil {
							log.Printf("[ERROR] - Error while copy file from %v to %v\n", tmpP, out)
							log.Fatal(err)
						} else {
							_, err = historyFile.WriteString("DONE - " + path + "\n")
							if err != nil {
								log.Println(err)
								log.Println("[ERROR] - Error writing history. No further history will be recorded.")
								err = historyFile.Close()
								if err != nil {
									log.Printf("[ERROR] - closing history file - %v", err)
								}
							}
						}

						log.Printf("[INFO ] - Copy file        (Done)\t%s\n", out)
					}
				}
			}
			log.Printf("+++++++++++++++++++++")
			return nil
		})
		if err != nil {
			log.Println(err)
		}
	}
	log.Printf(logBreakString) //Visual break in the log

	//Write filesList into log file
	for s, b := range filesList {
		log.Printf("%v\t- %v\n", b, s)
	}

	log.Printf(logBreakString) //Visual break in the log

	//Finalisation of the variable for the "CustomFiles" registry key value
	registryCustomFiles = registryCustomFiles + "  <ApplicationFile FileName=\"Genesys.Desktop.Modules.NewFacebookData.dll\" RelativePath=\"\" DataFile=\"false\" EntryPoint=\"false\" IsMainConfigFile=\"false\" Optional=\"false\" GroupName=\"\" />"
	registryCustomFiles = registryCustomFiles + "</ArrayOfApplicationFile>"
	log.Println(registryCustomFiles) //Write into log variable for the "CustomFiles" registry key value

	log.Printf(logBreakString) //Visual break in the log

	//Write "CustomFiles" into registry
	log.Println("[DEBUG] - Start write into registry")
	k, err := registry.OpenKey(registry.CURRENT_USER, `Software\Genesys\DeploymentManager`, registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		log.Fatal(err)
	}
	if err := k.SetStringValue("CustomFiles", registryCustomFiles); err != nil {
		log.Fatal(err)
	}
	if err := k.SetStringValue("AddCustomFile", "True"); err != nil {
		log.Fatal(err)
	}
	if err := k.Close(); err != nil {
		log.Fatal(err)
	}
	log.Println("[DEBUG] - Stop write into registry")

	//Run InteractionWorkspaceDeploymentManager
	err = os.Chdir(WDEPath)
	if err != nil {
		log.Printf("[ERROR] - Change working directory to current customization folder - %v", err)
	}
	err = os.Chdir("..\\InteractionWorkspaceDeploymentManager")
	if err != nil {
		log.Printf("[ERROR] - Change working directory to current customization folder - %v", err)
	}

	cmd := exec.Command("InteractionWorkspaceDeploymentManager.exe")
	err = cmd.Start()
	if err != nil {
		log.Fatal(err)
	}

	//close history file
	err = historyFile.Close()
	if err != nil {
		log.Println(err)
	}
}

func GetCustomFoldersList(folder string) ([]string, error) {
	log.Println("[DEBUG] - [GetCustomFoldersList] - Started")
	if folder == "" {
		log.Println("[DEBUG] - [GetCustomFoldersList] - Default path used")
		folder, _ = os.Getwd()
		log.Printf("[DEBUG] - [GetCustomFoldersList] - Default path - %v", folder)
	}
	files, err := ioutil.ReadDir(folder)
	if err != nil {
		log.Fatal(err)
		return nil, err
	}
	log.Println("[DEBUG] - [GetCustomFoldersList] - [Dir Readed]")
	foldersList := make([]string, 0, 20)
	for _, s := range files {
		log.Printf("[DEBUG] - [GetCustomFoldersList] - [ITEM] - %T %+v\n", s.Name(), s.Name())
		sss := filepath.Join(folder, s.Name())
		log.Printf("[DEBUG] - [GetCustomFoldersList] - [ITEM] - %T %+v\n", sss, sss)
		fi, err := os.Stat(sss)
		if err != nil {
			log.Println(err)
			return nil, err
		}
		switch mode := fi.Mode(); {
		case mode.IsDir():
			log.Printf("[DEBUG] - [GetCustomFoldersList] - Dir Finded - %s", s.Name())
			foldersList = append(foldersList, s.Name())
		}
	}
	log.Println("[DEBUG] - [GetCustomFoldersList] - Stoped")
	return foldersList, nil
}

func GetFileVersion(path string) FileVersion {
	size := w32.GetFileVersionInfoSize(path)
	if size <= 0 {
		log.Printf("GetFileVersionInfoSize failed - %v", path)
		return FileVersion{0, 0, 0, 0}
	}
	info := make([]byte, size)
	ok := w32.GetFileVersionInfo(path, info)
	if !ok {
		log.Printf("GetFileVersionInfo failed - %v", path)
		return FileVersion{0, 0, 0, 0}
	}
	fixed, ok := w32.VerQueryValueRoot(info)
	if !ok {
		log.Printf("VerQueryValueRoot failed - %v", path)
		return FileVersion{0, 0, 0, 0}
	}
	version := fixed.FileVersion()
	v1 := version & 0xFFFF000000000000 >> 48
	v2 := version & 0x0000FFFF00000000 >> 32
	v3 := version & 0x00000000FFFF0000 >> 16
	v4 := version & 0x000000000000FFFF >> 0
	log.Printf("file version: %d.%d.%d.%d\n", v1, v2, v3, v4)
	return FileVersion{uint16(v1), uint16(v2), uint16(v3), uint16(v4)}
}

func IsFileVersionGrater(newFileV FileVersion, oldFileV FileVersion) bool {
	log.Println("<><><>IsFileVersionGrater is started")
	defer log.Println("<><><>IsFileVersionGrater is stopped")
	switch {
	case newFileV.v1 > oldFileV.v1:
		log.Println("Version grater on v1")
		return true
	case newFileV.v2 > oldFileV.v2:
		log.Println("Version grater on v2")
		return true
	case newFileV.v3 > oldFileV.v3:
		log.Println("Version grater on v3")
		return true
	case newFileV.v4 > oldFileV.v4:
		log.Println("Version grater on v4")
		return true
	}
	log.Println("Version equal or less")
	return false
}
