//go:build pig_bedrock

package main

// pigTestBuildTags builds the test binary with the tags this test run has, so a pig_bedrock run starts a pig that links Bedrock.
const pigTestBuildTags = "pig_bedrock"
