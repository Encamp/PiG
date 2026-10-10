//go:build !pig_bedrock

package main

// pigTestBuildTags builds the test binary with the tags this test run has; an untagged run builds pig without Bedrock.
const pigTestBuildTags = ""
