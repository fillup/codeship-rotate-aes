# Automate rotation of Codeship Project AES keys

Codeship had a security incident recently that may have exposed the AES keys used by projects to provide encrypted
secrets to the build process for things like deployments, etc. As such any secrets in those files could have been 
exposed as well. See https://www.codeshipstatus.com/incidents/91bvlfw9xlm9 for more details.

As a team we manage hundreds of repositories used by dozens of applications and environments, so such an exposure like
this was a significant undertaking. Fortunately we use Terraform for all our infrastructure including AWS account 
credential management, so we were able to quickly disable any existing credentials and generate new ones. However, 
a list of new credentials still needed to be updated in our hundreds of Codeship projects. Codeship has an API and an
API client that supports getting lists of projects, current AES keys, and resetting the AES key. To speed up the 
rotation process we wrote this script to deal with the Codeship rotation portion. Specifically what it does is outlined
next.

## Rotation process:
 - Get list of Codeship Pro projects
 - For each project:
   - Clone repo
   - Check files for files matching a given pattern, example `*.encrypted`
   - If present, use AES key API response to decrypt file
   - Iterate through a list of strings for substitution and make any substitutions available in file
   - Call API to reset AES key
   - Encrypt file using new AES key
   - Delete decrypted files and AES key file
   - Checkout new local branch (optional)
   - Add encrypted files
   - Commit changes
   - Push changes (optionally to new branch)
   - Delete cloned repo folder
   - Add project name to local file `completed-projects.txt` to prevent processing it again on another run of script
 - Display list of URLs for creating pull requests for pushed branches
 - Display list of projects processed along with lists of encrypted files found in each and the number of changes
   made in each file. 

## WARNING!!!
This script was not written to be any kind of example of quality Go code. It works though. I used it to process 115
projects on Codeship and am confident enough to share it now. It works for my situation and may not work for yours. If 
you have updates you'd like to make feel free to submit PRs and as long as it doesn't break stuff for me I'll likely 
merge it in. 

Also, I am not responsible if this code causes you any problems. Like I said, I ran it on a lot of projects successfully 
hosted both on GitHub and Bitbucket. I have not tried it with Gitlab or any other Git hosting provider.  

## Requirements
This script makes heavy use of executing commands on the host OS for interacting with Git and Jet (Codeship's CLI). It 
also uses OS filesystem calls to get lists of files in the repo and remove files and folders for cleanup. To use this 
app you must have locally available:

 1. Git
 2. Jet - https://documentation.codeship.com/pro/jet-cli/installation/
 3. Environment variables for Codeship credentials:
   - `CODESHIP_USERNAME`
   - `CODESHIP_PASSWORD`
   - `CODESHIP_ORGANIZATION`
 4. You must disable 2-Step Verification on your Codeship account while using this script, API access does not work 
    when 2SV is enabled. 
    
## Configuration:
Configuration is done in a local file named `config.json`. You can run this script without one and it'll generate the 
file for you with all the options. Here is a description of each option:

 - `encrypted_file_patterns` - An array of regex to use to identify encrypted files in repos. We always just fig ours 
   a suffix of `.encrypted` so the pattern we used is `\\.encrypted$`
 - `replacements` - A map of strings to look for and what to replace them with. This is where you'd have the old 
   access key and new access key, etc.
 - `checkout_branch` - If you want a specific branch, such as `develop` checked out before changes are made and 
   committed, put the name here.
 - `push_branch` - If you'd like the changes pushed to a new branch, put the name here.
 - `max_projects_per_run` - If you like a little safety net you can limit how many projects will be processed with each
   run of the script. Start with 1 and increase from there as you see how it works for you.
 - `repo_filter_patterns` - An array of regex to use to match on repository urls to determine which to process. For 
   example you can put a specific Organization or account name, or a partial match for some set of repos, etc.
 - `reset_keys_in_projects_without_encrypted_files` - Yep, it's a long name, but I wanted it clear. This is a boolean
   to determine if the Codeship project AES key should be reset even if the repo doesn't have any encrypted files. 
   Right now this would still be a good thing to do given existing keys were potentially exposed. 
   
## Usage:
Considering this code is doing some scary stuff I personally would not run a provided binary from someone else. So 
instead of providing a binary you can either run this with Go locally if you are set up to do so, or use
Docker with the `docker-compose.yml` configuration to run in a container.

When configured and running, once it builds the list of projects it'll process that list is displayed and then the 
script sleeps for 20 seconds to give you a chance to Ctrl-C and cancel the process. Once running you can watch the 
fairly verbose logging that happens and watch for any errors or alerts. When complete a list of PR urls will be 
displayed and a list of the names and numbers of files updated for each project. The list of PRs is just a list of 
URLs generated using a template, they may not be right depending on your branch names and strategies, so be aware.

### With Go locally:
 1. Clone the repo
 2. Run `go run .`
 3. That'll create the base `config.json` file. Update it.
 4. Run `go run .` and watch the output
 
### With Docker:
 1. Clone the repo
 2. Copy `.env.example` to `.env` and update with Codeship credentials
 3. Run `docker-compose run rotate`
 4. That'll create the base `config.json` file. Update it.
 5. Run `docker-compose run rotate`
 
## Note about codeship-go library
The official Codeship Go client library was missing support for the Reset AES Key endpoint so I've added it in a fork
and am PRing it back to the main repo. You can review it here: https://github.com/codeship/codeship-go/pull/59.

Also there were some Go module version errors that required a couple "replace"
statements in the go.mod file to get them to work properly. 

## MIT License

Copyright (c) 2020 Phillip Shipley

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.