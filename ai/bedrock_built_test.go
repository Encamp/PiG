//go:build pig_bedrock

package ai

// BedrockBuilt reports whether this test binary links Amazon Bedrock. Untagged, the provider matrices leave out their Bedrock rows; the pig_bedrock build runs them.
const BedrockBuilt = true
