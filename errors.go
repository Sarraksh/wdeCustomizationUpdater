package main

import "fmt"

var ErrCustomFilesNotFound = fmt.Errorf("not found CustomFiles key in old registry data \"RegistryValues\"")
var ErrVersionNotExist = fmt.Errorf("version not exsist")
var ErrNoFilesFoundInFolderByPattern = fmt.Errorf("folder contains no files")
