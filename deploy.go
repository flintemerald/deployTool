package main

import (
  "bufio"
  "fmt"
  "os"
  "os/exec"
  "encoding/json"
  "io/ioutil"
  "path"
  "strings"
  "golang.org/x/crypto/ssh"
  "github.com/pkg/sftp"
)



const constLogStatusShift = 60



var sshCon *ssh.Client
var sftpCon *sftp.Client



type dynamicMap map[string]interface{}

func (dm *dynamicMap) getBool(key string, def bool) bool {
  switch (*dm)[key].(type) {
  case bool:
    return (*dm)[key].(bool)
  default:
    return def
  }
}

func (dm *dynamicMap) getString(key string) string {
  switch (*dm)[key].(type) {
  case string:
    return (*dm)[key].(string)
  default:
    panic("key missing: " + key)
  }
}

func (dm *dynamicMap) getStringList(key string) []string {
  unkList := (*dm)[key].([]interface{})
  var strList []string
  for _, unk := range unkList {
    strList = append(strList, unk.(string))
  }
  return strList
}



func maxInt(a, b int) int {
  if a > b {
    return a
  }
  return b
}



func main() {
  if len(os.Args) > 1 {
    configFile := os.Args[1]
    configString, errRead := ioutil.ReadFile(configFile)
    if errRead != nil {
      fmt.Printf("error: unable to read file %v\n", configFile)
      fmt.Println(errRead)
      return
    }
    configMap := make(dynamicMap)
    errUnmarshal := json.Unmarshal(configString, &configMap)
    if errUnmarshal != nil {
      fmt.Printf("error: unable to parse json\n")
      fmt.Println(errUnmarshal)
      return
    }
    
    serverList := configMap["serverList"].([]interface{})
    deployPath := configMap["deployPath"].(string)
    needConfirm := configMap["needConfirm"].(bool)
    
    printHeader(configMap)
    
    if needConfirm {
      confirmWarning := configMap["confirmWarning"].(string)
      fmt.Print(confirmWarning)
      reader := bufio.NewReader(os.Stdin)
      yesText, _ := reader.ReadString('\n')
      if yesText != "yes\n" {
        fmt.Print("canceled\n\n")
        os.Exit(0)
      } else {
        fmt.Print("\n")
      }
    }
    
    fmt.Printf("deployPath: %v\n", deployPath)
    execPreDeployLocal(configMap)
    serversCount := len(serverList)
    serversDone := 0
    for _, srv := range serverList {
      var srvDm dynamicMap = srv.(map[string]interface{})
      if sshConnect(&srvDm) {
        sftpConnect()
        execRemoteCommandEx("mkdir "+deployPath, false, true)
        execPreDeployRemote(configMap)
        doDeploy(configMap)
        execPostDeployRemote(configMap)
        sftpDisconnect()
        sshDisconnect()
        serversDone += 1
      }
    }
    execPostDeployLocal(configMap)
    fmt.Println("--------------------------------------------------")
    if serversDone == serversCount {
      fmt.Printf("done! %v of %v servers has deployed\n", serversDone, serversCount)
    } else {
      fmt.Printf("done with WARNING: %v of %v servers has deployed\n", serversDone, serversCount)
    }
  } else {
    fmt.Println("usage: deploy <config_file.json>")
  }
}



func printHeader(configMap dynamicMap) {
  fmt.Println(configMap["headerMessage"])
}



func execPreDeployLocal(configMap dynamicMap) {
  for _, cmdObj := range configMap["preDeployLocalCommands"].([]interface{}) {
    var cmdMap dynamicMap
    cmdMap = cmdObj.(map[string]interface{})
    required := cmdMap.getBool("required", false)
    cmdString := cmdMap["cmd"].(string)
    execLocalCommand(cmdString, required)
  }
}



func execPreDeployRemote(configMap dynamicMap) {
  for _, cmdObj := range configMap["preDeployRemoteCommands"].([]interface{}) {
    var cmdMap dynamicMap
    cmdMap = cmdObj.(map[string]interface{})
    required := cmdMap.getBool("required", false)
    cmdString := cmdMap["cmd"].(string)
    execRemoteCommand(cmdString, required)
  }
}



func doDeploy(configMap dynamicMap) {
  deployPath := configMap["deployPath"].(string)
  skipMaskList := configMap.getStringList("skipMaskList")
  for _, fileLocalUnk := range configMap["deployItems"].([]interface{}) {
    var fileLocalPath string = fileLocalUnk.(string)
    isDirStat, err := os.Stat(fileLocalPath)
    if err == nil {
      isDir := isDirStat.IsDir()
      if isDir {
        uploadDir(fileLocalPath, deployPath, skipMaskList)
      } else {
        uploadFile(fileLocalPath, deployPath, skipMaskList)
      }
    } else {
      printWithStatus(fmt.Sprintf(" -> %v", fileLocalPath), "missing!\n", false)
    }
  }
}



func uploadDir(dirLocalPath string, deployPath string, skipMaskList []string) {
  if isFileIgnored(dirLocalPath, skipMaskList) {
    return
  }
  execRemoteCommandEx("mkdir " + deployPath + "/" + dirLocalPath, false, true)
  files, _ := ioutil.ReadDir(dirLocalPath)
  for _, file := range files {
    if file.IsDir() {
      dirPath := dirLocalPath + "/" + file.Name()
      uploadDir(dirPath, deployPath, skipMaskList)
    } else {
      fileName := dirLocalPath + "/" + file.Name()
      uploadFile(fileName, deployPath, skipMaskList)
    }
  }
}



func uploadFile(filePath string, deployPath string, skipMaskList []string) {
  printWithStatus(fmt.Sprintf(" -> %v", filePath), "...", false)
  if isFileIgnored(filePath, skipMaskList) {
    printWithStatus(fmt.Sprintf(" -> %v", filePath), "ignored\n", true)
    return
  }
  srcFile, _ := os.Open(filePath)
  dstFile, _ := sftpCon.Create(deployPath + "/" + filePath)
  buf := make([]byte, 256*1024)
  bytes := 0
  for {
    n, _ := srcFile.Read(buf)
    bytes += n
    printWithStatus(fmt.Sprintf(" -> %v", filePath), fmt.Sprintf("...(%v kb)", bytes/1024), true)
    if n == 0 {
      break
    }
    dstFile.Write(buf[:n])
  }
  printWithStatus(fmt.Sprintf(" -> %v", filePath), fmt.Sprintf("ok (%v kb)\n", bytes/1024), true)
}



func isFileIgnored(filePath string, skipMaskList []string) bool {
  for _, mask := range skipMaskList {
    isIgnored, _ := path.Match(mask, path.Base(filePath))
    if isIgnored {
      return true
    }
  }
  return false
}



func execPostDeployRemote(configMap dynamicMap) {
  for _, cmdObj := range configMap["postDeployRemoteCommands"].([]interface{}) {
    var cmdMap dynamicMap
    cmdMap = cmdObj.(map[string]interface{})
    required := cmdMap.getBool("required", false)
    cmdString := cmdMap["cmd"].(string)
    execRemoteCommand(cmdString, required)
  }
}



func execPostDeployLocal(configMap dynamicMap) {
  for _, cmdObj := range configMap["postDeployLocalCommands"].([]interface{}) {
    var cmdMap dynamicMap
    cmdMap = cmdObj.(map[string]interface{})
    required := cmdMap.getBool("required", false)
    cmdString := cmdMap["cmd"].(string)
    execLocalCommand(cmdString, required)
  }
}



func printWithStatus(text string, status string, update bool) {
  logStatusShift := constLogStatusShift - len(text)
  logStatusShift = maxInt(3, logStatusShift)
  if update {
    fmt.Print("\r")
  }
  fmt.Print(text)
  for i := 0; i < logStatusShift; i+=1 {
    fmt.Print(" ")
  }
  fmt.Print(status)
}



func execLocalCommand(cmdString string, required bool) {
  printWithStatus(fmt.Sprintf("local exec: %v", cmdString), "...", false)
  cmdArgs := strings.Split(cmdString, " ")
  cmd := exec.Command(cmdArgs[0], cmdArgs[1:]...)
  _, err := cmd.Output()
  if err != nil {
    if required {
      printWithStatus(fmt.Sprintf("local exec: %v", cmdString), "failed \n", true)
      fmt.Printf("error: unable to exec command: %v\n", cmdString)
      fmt.Println(err)
      os.Exit(3)
    } else {
      printWithStatus(fmt.Sprintf("local exec: %v", cmdString), "skipped\n", true)
    }
  } else {
    printWithStatus(fmt.Sprintf("local exec: %v", cmdString), "ok     \n", true)
  }
}



func execRemoteCommand(cmdString string, required bool) {
  execRemoteCommandEx(cmdString, required, false)
}



func execRemoteCommandEx(cmdString string, required bool, silent bool) {
  if !silent {printWithStatus(fmt.Sprintf("remote exec: %v", cmdString), "...", false)}
  sshSession, _ := sshCon.NewSession()
  err := sshSession.Run(cmdString)
  if err != nil {
    if required {
      if !silent {printWithStatus(fmt.Sprintf("remote exec: %v", cmdString), "failed \n", true)}
      panic("error: unable to exec command " + cmdString + ", err: " + err.Error())
    } else {
      if !silent {printWithStatus(fmt.Sprintf("remote exec: %v", cmdString), "skipped\n", true)}
    }
  } else {
    if !silent {printWithStatus(fmt.Sprintf("remote exec: %v", cmdString), "ok     \n", true)}
  }
  sshSession.Close()
}



func sshConnect(srv *dynamicMap) bool {
  config := &ssh.ClientConfig{}
  config.User = srv.getString("user")
  config.Auth = []ssh.AuthMethod{ssh.Password(srv.getString("pswd"))}
  config.HostKeyCallback = ssh.InsecureIgnoreHostKey()
  host := srv.getString("host")
  printWithStatus(fmt.Sprintf("[[[ssh connect to %v", host), "...     ", false)
  var err error
  sshCon, err = ssh.Dial("tcp", host, config)
  if err != nil {
    printWithStatus(fmt.Sprintf("warning: unable connect to server %v", host), "skipped\n", true)
    return false
  }
  printWithStatus(fmt.Sprintf("[[[ssh connect to %v", host), "ok      \n", true)
  return true
}



func sshDisconnect() {
  printWithStatus("]]]ssh close", "...     ", false)
  sshCon.Close()
  printWithStatus("]]]ssh close", "ok      \n", true)
}



func sftpConnect() {
  printWithStatus("[[[sftp connect", "...     ", false)
  var err error
  sftpCon, err = sftp.NewClient(sshCon)
  if err != nil {
    panic("error: unable to establish sftp transport, err: " + err.Error())
  }
  printWithStatus("[[[sftp connect", "ok      \n", true)
}



func sftpDisconnect() {
  printWithStatus("]]]sftp close", "...     ", false)
  sftpCon.Close()
  printWithStatus("]]]sftp close", "ok      \n", true)
}
