package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/codeship/codeship-go"
)

var changeCounts = map[string]map[string]int{}
var DefaultMaxProjectsPerRun = 20
var prURLS []string

type Config struct {
	EncryptedFilePatterns                    []string          `json:"encrypted_file_patterns"`
	Replacements                             map[string]string `json:"replacements"`
	CheckoutBranch                           string            `json:"checkout_branch"`
	PushBranch                               string            `json:"push_branch"`
	MaxProjectsPerRun                        int               `json:"max_projects_per_run"`
	RepoFilterPatterns                       []string          `json:"repo_filter_patterns"`
	ResetKeysInProjectsWithoutEncryptedFiles bool              `json:"reset_keys_in_projects_without_encrypted_files"`
}

var config Config
var emptyConfig = Config{EncryptedFilePatterns: []string{""}, Replacements: map[string]string{"find": "replace"}, RepoFilterPatterns: []string{}}

func init() {
	// load config file or create a template file if config.json doesn't exist
	data, err := ioutil.ReadFile("config.json")
	if err != nil {
		if os.IsNotExist(err) {
			cfjson, err := json.MarshalIndent(emptyConfig, "", "  ")
			if err != nil {
				log.Fatalf("a local config file was not found and we were unable to marshal a template file, error: %s", err)
			}
			if err := ioutil.WriteFile("config.json", cfjson, 0600); err != nil {
				log.Fatalf("a local config file was not found and got an error trying to write a template file: %s", err)
			}
			fmt.Println("a local config file was not found, so one was created for you. update it and run again")
			os.Exit(1)
		}
		log.Fatalf("failed to load config file: %s", err)
	}

	if err := json.Unmarshal(data, &config); err != nil {
		log.Fatalf("failed to load config.json into config struct: %s", err)
	}

	if config.MaxProjectsPerRun == 0 {
		config.MaxProjectsPerRun = DefaultMaxProjectsPerRun
	}
}

func main() {
	auth := codeship.NewBasicAuth(os.Getenv("CODESHIP_USERNAME"), os.Getenv("CODESHIP_PASSWORD"))
	client, err := codeship.New(auth)
	if err != nil {
		log.Fatal(err)
	}

	ctx := context.Background()
	org, err := client.Organization(ctx, os.Getenv("CODESHIP_ORGANIZATION"))
	if err != nil {
		log.Fatal(err)
	}

	completedProjects := getCompletedProjects()

	var allProjects []codeship.Project

	// get first page of projects
	projects, resp, err := org.ListProjects(ctx)
	if err != nil {
		log.Fatal(err)
	}
	for _, p := range projects.Projects {
		if p.Type == codeship.ProjectTypePro && !isStringInSlice(p.Name, completedProjects) {
			if len(config.RepoFilterPatterns) > 0 {
				matched := false
				for _, repoPattern := range config.RepoFilterPatterns {
					re := regexp.MustCompile(repoPattern)
					if re.MatchString(p.RepositoryURL) {
						matched = true
						break
					}
				}
				if !matched {
					continue
				}
			}

			if len(allProjects) >= config.MaxProjectsPerRun {
				break
			}
			allProjects = append(allProjects, p)
		}
	}

	// process rest of pages until max projects per run is met
loop:
	for {
		if resp.IsLastPage() || resp.Next == "" {
			break
		}

		next, _ := resp.NextPage()

		projects, resp, err = org.ListProjects(ctx, codeship.Page(next), codeship.PerPage(50))
		if err != nil {
			log.Fatal(err)
		}
		for _, p := range projects.Projects {
			if p.Type == codeship.ProjectTypePro && !isStringInSlice(p.Name, completedProjects) {
				if len(config.RepoFilterPatterns) > 0 {
					matched := false
					for _, repoPattern := range config.RepoFilterPatterns {
						re := regexp.MustCompile(repoPattern)
						if re.FindString(p.RepositoryURL) != "" {
							matched = true
							break
						}
					}
					if !matched {
						continue
					}
				}

				if len(allProjects) >= config.MaxProjectsPerRun {
					break loop
				}
				allProjects = append(allProjects, p)
			}
		}
	}

	if len(allProjects) == 0 {
		fmt.Println("no projects to be rotated")
		os.Exit(0)
	}

	fmt.Printf("Found %v projects:\n", len(allProjects))
	for i := range allProjects {
		fmt.Printf("  %v - %s\n", i+1, allProjects[i].Name)
	}

	fmt.Println("Will sleep for 20 seconds, so bail now or forever hold your peace...")
	time.Sleep(20 * time.Second)

	wd, err := os.Getwd()
	if err != nil {
		log.Fatalf("unable to get current working directory: %s", err)
	}

	for i, p := range allProjects {
		fmt.Printf("\n\n--------------------------------------------------------\n")
		fmt.Printf("Starting project #%v - %s\n", i+1, p.Name)
		changeCounts[p.Name] = map[string]int{}

		if err := os.Chdir(wd); err != nil {
			log.Fatalf("unable to change back to main working directory %s, error: %s", wd, err)
		}

		folder, err := cloneProject(p)
		if err != nil {
			fmt.Printf("ALERT!!!! failed to clone project so it was not processed, fix it manually: %s\n", err)
			continue
		}
		fmt.Printf("Project cloned into %s\n", folder)

		if err := os.Chdir(folder); err != nil {
			fmt.Printf("ALERT!!!! failed to change dir into project %s so it was not processed, fix it manually: %s\n", folder, err)
			continue
		}

		encFiles := findEncryptedFiles(getFileList("."), config.EncryptedFilePatterns)
		if len(encFiles) == 0 {
			fmt.Printf("no encrypted files found for project %s\n", p.Name)
		} else {
			fmt.Printf("found encrypted files: \n%s\n", strings.Join(encFiles, "\n"))
		}

		if len(encFiles) == 0 && !config.ResetKeysInProjectsWithoutEncryptedFiles {
			fmt.Printf("since no encrypted files were found, will not rotate key, proceeding to next project...\n")
			if err := addCompletedProject(p.Name); err != nil {
				fmt.Printf("failed to add project to completed projects file: %s", err)
			}

			if err := removeFolder(folder); err != nil {
				fmt.Println(err.Error())
			}
			continue
		}

		var aesFile string
		if len(encFiles) > 0 {
			aesFile, err = createAESFile(".", p.AesKey)
			if err != nil {
				fmt.Printf("failed to create AES file %s for project %s, error: %s\n", aesFile, p.Name, err)
			}

			for _, file := range encFiles {
				if err := decryptFile(file, aesFile); err != nil {
					fmt.Printf("failed to decrypt %s, error: %s\n", file, err)
					if err := cleanupFolder("."); err != nil {
						fmt.Printf("%s", err)
					}
					continue
				}

				if err := replaceSecretsInFile(file+".decrypted", config.Replacements, p.Name); err != nil {
					fmt.Printf("failed to replace secrets in file %s, error: %s\n", file, err)
					if err := cleanupFolder("."); err != nil {
						fmt.Printf("%s", err)
					}
					continue
				}
			}
		}

		updated, _, err := org.ResetProjectAESKey(ctx, p.UUID)
		if err != nil {
			fmt.Printf("failed to reset AES key for project %s on Codeship: %s\n", p.Name, err)
			if err := cleanupFolder(folder); err != nil {
				fmt.Printf("%s", err)
			}
			continue
		}

		if len(encFiles) > 0 {
			if err := removeFile(aesFile); err != nil {
				fmt.Printf("unable to delete previous aes file after resetting project aes key: %s\n", err)
				fmt.Printf("UH OH!!!, manual intervention required. you'll need to decrypt files with old key (%s) and renecrypt with new key (%s)\n", p.AesKey, updated.AesKey)
				continue
			}

			updatedAesFile, err := createAESFile(".", updated.AesKey)
			if err != nil {
				fmt.Printf("failed to create updated AES file %s for project %s, error: %s\n", updatedAesFile, p.Name, err)
			}

			for _, file := range encFiles {
				if err := encryptFile(file, aesFile); err != nil {
					fmt.Printf("failed to encrypt %s, error: %s\n", file, err)
				}
			}

			if err := cleanupFolder("."); err != nil {
				fmt.Printf("ALERT: unable to cleanup folder before pushing branch, WONT PUSH AUTOMATICALY!!!%s\n", err)
				continue
			}

			if err := commitAndPushNewBranch(); err != nil {
				fmt.Printf("got an error in commit and push process, YOU PROBABLY NEED TO PUSH MANUALLY!!!: %s\n", err)
			}

			prURLS = append(prURLS, getPRURL(p.RepositoryURL, p.Name))
		}

		if err := os.Chdir(".."); err != nil {
			log.Fatalf("Unable to change directory up a level, error: %s", err)
		}

		if err := addCompletedProject(p.Name); err != nil {
			fmt.Printf("failed to add project to completed projects file: %s", err)
		}

		if err := removeFolder(folder); err != nil {
			fmt.Println(err.Error())
		}

		fmt.Printf("\n\nFinished process for %s project!!!\n", p.Name)
		fmt.Printf("--------------------------------------------------------\n")
	}

	fmt.Printf("\n\nall projects complete, now go create some PRs:\n")
	for _, url := range prURLS {
		fmt.Println(url)
	}

	fmt.Printf("\n\nChange counts by project and file:\n")
	for projectName, data := range changeCounts {
		fmt.Printf("  %s:\n", projectName)
		for file, count := range data {
			fmt.Printf("    %s - %v\n", file, count)
		}
	}

	fmt.Printf("adios amigo\n")
}

func cloneProject(project codeship.Project) (string, error) {
	cloneUrl := getGitCloneUrl(project)
	folder := getFolderName(project)
	fmt.Printf("Preparing to clone %s into %s...\n", cloneUrl, folder)
	cmd := exec.Command("git", "clone", cloneUrl)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("failed to clone repo %s, error: %s, output: %s", cloneUrl, err, out.String())
	}

	if err := os.Chdir(folder); err != nil {
		return "", fmt.Errorf("failed to change dir into %s after clone, error: %s", folder, err)
	}

	if config.CheckoutBranch != "" {
		cmd2 := exec.Command("git", "checkout", config.CheckoutBranch)
		var out2 bytes.Buffer
		cmd2.Stdout = &out2
		cmd2.Stderr = &out2
		if err := cmd2.Run(); err != nil {
			return "", fmt.Errorf("failed to checkout branch %s, error: %s, output: %s", config.CheckoutBranch, err, out2.String())
		}
	}

	if err := os.Chdir(".."); err != nil {
		return "", fmt.Errorf("failed to change dir up a level after branch checkout, error: %s", err)
	}

	return folder, nil
}

func commitAndPushNewBranch() error {
	type command struct {
		name    string
		command *exec.Cmd
	}

	commands := []command{}

	if config.PushBranch != "" {
		commands = append(commands, command{
			name:    "checkout new branch",
			command: exec.Command("git", "checkout", "-b", config.PushBranch),
		})
	}

	commands = append(commands,
		command{
			name:    "add encrypted files",
			command: exec.Command("git", "add", "*.encrypted"),
		},
		command{
			name:    "commit changes",
			command: exec.Command("git", "commit", "-m", "updated encrypted files with rotated credentials"),
		})

	if config.PushBranch != "" {
		commands = append(commands, command{
			name:    "push new branch",
			command: exec.Command("git", "push", "-u", "origin", config.PushBranch),
		})
	} else {
		commands = append(commands, command{
			name:    "push changes",
			command: exec.Command("git", "push"),
		})
	}

	for _, cmd := range commands {
		var out bytes.Buffer
		cmd.command.Stderr = &out
		cmd.command.Stdout = &out
		if err := cmd.command.Run(); err != nil {
			branch := config.CheckoutBranch
			if config.PushBranch != "" {
				branch = config.PushBranch
			}
			return fmt.Errorf("failed to %s, branch: %s, error: %s, output: %s", cmd.name, branch, err, out.String())
		}
		fmt.Printf("push process command %s executed successfully\n", cmd.name)
	}

	return nil
}

func getGitCloneUrl(project codeship.Project) string {
	var domain string
	switch strings.ToLower(project.RepositoryProvider) {
	case "github":
		domain = "github.com"
	case "bitbucket":
		domain = "bitbucket.org"
	}

	if domain == "" {
		return ""
	}

	return fmt.Sprintf("git@%s:%s.git", domain, project.Name)
}

func getPRURL(repoURL, projectName string) string {
	if strings.Contains(repoURL, "bitbucket") {
		return fmt.Sprintf("https://bitbucket.org/%s/pull-requests/new?source=develop&t=1", projectName)
	}
	if strings.Contains(repoURL, "github") {
		return fmt.Sprintf("https://github.com/%s/compare/master...develop", projectName)
	}
	return ""
}

func getFolderName(project codeship.Project) string {
	parts := strings.Split(project.Name, "/")
	return parts[1]
}

func getFileList(folder string) []string {
	files, err := ioutil.ReadDir(folder)
	if err != nil {
		log.Fatal(err)
	}

	onlyFiles := []string{}

	for _, file := range files {
		if file.IsDir() {
			continue
		}
		onlyFiles = append(onlyFiles, file.Name())
	}

	return onlyFiles
}

func findEncryptedFiles(files []string, patterns []string) []string {
	encFiles := []string{}
	for i := range files {
		for _, pattern := range patterns {
			re := regexp.MustCompile(pattern)
			if re.MatchString(files[i]) {
				encFiles = append(encFiles, files[i])
			}
		}
	}
	return encFiles
}

func createAESFile(folder, key string) (string, error) {
	if folder == "" {
		folder = "."
	}
	filename := fmt.Sprintf("%s/%s", folder, "codeship.aes")
	fmt.Printf("creating AES key file: %s\n", filename)
	return filename, ioutil.WriteFile(filename, []byte(key), 06400)
}

func decryptFile(file, keyFile string) error {
	fmt.Printf("Decrypting %s to %s.decrypted ...", file, file)
	cmd := exec.Command("jet", "decrypt", "--key-path", keyFile, file, file+".decrypted")
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to decrypt %s, error: %s, output: %s", file, err, out.String())
	}
	fmt.Printf("done\n")

	return nil
}

func encryptFile(file, keyFile string) error {
	fmt.Printf("Encrypting %s.decrypted to %s ...", file, file)
	cmd := exec.Command("jet", "encrypt", "--key-path", keyFile, file+".decrypted", file)
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to encrypt %s, error: %s, output: %s", file, err, out.String())
	}
	fmt.Printf("done\n")

	return nil
}

func replaceSecretsInFile(file string, replacements map[string]string, projectName string) error {
	contents, err := ioutil.ReadFile(file)
	if err != nil {
		return err
	}

	strconts := string(contents)
	matches := 0
	for key, val := range replacements {
		if strings.Contains(strconts, key) {
			matches++
		}
		strconts = strings.Replace(strconts, key, val, -1)
		changeCounts[projectName][file] = matches
	}
	fmt.Printf("Replaced %v strings in %s\n", matches, file)
	return ioutil.WriteFile(file, []byte(strconts), 06400)
}

func removeFile(file string) error {
	if err := os.Remove(file); err != nil {
		return fmt.Errorf("FAILED TO DELETE FILE: %s\n", file)
	}

	return nil
}

func removeFolder(folder string) error {
	cmd := exec.Command("rm", "-rf", folder)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to remove repo folder %s, error: %s, output: %s", folder, err, out.String())
	}
	return nil
}

func cleanupFolder(folder string) error {
	// remove codeship.aes
	if err := removeFile(folder + "/codeship.aes"); err != nil {
		return err
	}
	fmt.Printf("deleted %s/codeship.aes\n", folder)

	// remove any .decrypted files
	files := getFileList(folder)
	for _, filename := range files {
		if strings.HasSuffix(filename, "decrypted") {
			if err := removeFile(folder + "/" + filename); err != nil {
				return err
			}
			fmt.Printf("deleted %s/%s\n", folder, filename)
		}
	}
	return nil
}

func getCompletedProjects() []string {
	list, err := ioutil.ReadFile("completed-projects.txt")
	if err != nil {
		return []string{}
	}
	return strings.Split(string(list), "\n")
}

func addCompletedProject(name string) error {
	f, err := os.OpenFile("completed-projects.txt",
		os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("failed to open completed-projects.txt, error: %s", err)
	}
	defer f.Close()
	if _, err := f.WriteString(name + "\n"); err != nil {
		return fmt.Errorf("failed to append project %s to completed-projects.txt, error: %s", name, err)
	}
	return nil
}

// IsStringInSlice iterates over a slice of strings, looking for the given
// string. If found, true is returned. Otherwise, false is returned.
func isStringInSlice(needle string, haystack []string) bool {
	for _, hs := range haystack {
		if needle == hs {
			return true
		}
	}

	return false
}
