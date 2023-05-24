package main

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

type CitationMetadata struct {
	Title string `json:"title"`
	Url   string `json:"url"`
	Text  string `json:"text"`
}

type Citation struct {
	StartIx  int              `json:"start_ix"`
	EndIx    int              `json:"end_ix"`
	Metadata CitationMetadata `json:"metadata"`
}

type Metadata struct {
	Timestamp     string       `json:"timestamp_"`
	MessageType   interface{}  `json:"message_type"`
	FinishDetails interface{}  `json:"finish_details"`
	CiteMetadata  CiteMetadata `json:"_cite_metadata"`
	Citations     []Citation   `json:"citations"`
}

type MetaDataListItem struct {
	Title string `json:"title"`
	Url   string `json:"url"`
}

type CitationFormat struct {
	Name string `json:"name"`
}

type CiteMetadata struct {
	CitationFormat CitationFormat     `json:"citation_format"`
	MetaDataList   []MetaDataListItem `json:"metadata_list"`
}

type Block struct {
	Text      string `json:"-"`
	ClickList []string
	UrlList   []string
}

func ReplaceSourcesWithUrls(text string, urls []string) string {
	// Find all "【num†source】" in the text
	re := regexp.MustCompile(`【\d+†source】`)
	matches := re.FindAllString(text, -1)

	index := 0
	// Iterate over each match
	for _, match := range matches {
		// Check if number is within urls array bounds
		if index < len(urls) && index >= 0 {
			text = regexp.MustCompile(match).ReplaceAllString(text, " __["+fmt.Sprint(index+1)+"]("+urls[index]+")__")
		} else {
			fmt.Println("Number in 'click' is out of URLs array bounds")
		}
		index++
	}

	return text
}

func ReplaceClickWithUrl(text string, urls []string) string {
	// Find all "click(number)" in the text
	re := regexp.MustCompile(`click\(\d+\)`)
	matches := re.FindAllString(text, -1)

	// Iterate over each match
	for _, match := range matches {
		// Extract number from "click(number)"
		reNum := regexp.MustCompile(`\d+`)
		num := reNum.FindString(match)

		// Convert string number to int
		numInt, err := strconv.Atoi(num)
		if err != nil {
			fmt.Println("Failed to convert string to int: ", err)
			continue
		}

		// Check if number is within urls array bounds
		if numInt < len(urls) && numInt >= 0 {
			// Replace "number" with corresponding url
			text = regexp.MustCompile(`click\(`+num+`\)`).ReplaceAllString(text, `click("[`+urls[numInt]+`](`+urls[numInt]+`)")`)
		} else {
			fmt.Println("Number in 'click' is out of URLs array bounds")
		}
	}

	return text
}

func GPT4ReplaceMetadata(inputText string, cleanCodeBlocks bool) string {
	// Find all blocks with CODE_METADATA
	inputText = strings.ReplaceAll(inputText, "\n", "%NEW_LINE%")

	re := regexp.MustCompile("```(.+?)%%%CODE_METADATA:(.+?)%%%```")
	blockMatches := re.FindAllStringSubmatch(inputText, -1)

	// Parse each block's CODE_METADATA
	var blocks []Block
	for i, blockMatch := range blockMatches {
		inputText = strings.Replace(inputText, blockMatch[0], "///CODE_BLOCK_PLACEHOLDER"+fmt.Sprint(i)+"///", 1)
		metadataString := blockMatch[2]
		metadata := Metadata{}
		var urlList []string
		err := json.Unmarshal([]byte(metadataString), &metadata)
		if err != nil {
			panic(err)
		}
		for _, item := range metadata.CiteMetadata.MetaDataList {
			urlList = append(urlList, item.Url)
		}
		blocks = append(blocks, Block{
			Text:    blockMatch[1],
			UrlList: urlList,
		})
	}

	// Replace click(number) with corresponding URL
	for i, block := range blocks {
		blocks[i].Text = ReplaceClickWithUrl(block.Text, block.UrlList)
	}

	// Remove {{{...}}}
	for i, block := range blocks {
		blocks[i].Text = strings.ReplaceAll(block.Text, "%%%CODE_METADATA:(.*?)%%%", "")
	}

	for i, block := range blocks {
		if cleanCodeBlocks || strings.Contains(block.Text, "quote(") {
			inputText = strings.Replace(inputText, "///CODE_BLOCK_PLACEHOLDER"+fmt.Sprint(i)+"///", "", 1)
		} else {
			inputText = strings.Replace(inputText, "///CODE_BLOCK_PLACEHOLDER"+fmt.Sprint(i)+"///", block.Text, 1)
		}
	}

	re = regexp.MustCompile("%%%TEXT_METADATA:(.+?)%%%")
	blockMatches = re.FindAllStringSubmatch(inputText, -1)

	// Parse each block's TEXT_METADATA
	blocks = []Block{}
	var urlList []string
	for i, blockMatch := range blockMatches {
		inputText = strings.Replace(inputText, blockMatch[0], "///TEXT_BLOCK_PLACEHOLDER"+fmt.Sprint(i)+"///", 1)
		metadataString := blockMatch[1]
		metadata := Metadata{}

		err := json.Unmarshal([]byte(metadataString), &metadata)
		if err != nil {
			fmt.Println(metadataString)
			panic(err)
		}
		for _, item := range metadata.Citations {
			urlList = append(urlList, item.Metadata.Url)
		}
		blocks = append(blocks, Block{
			Text:    blockMatch[1],
			UrlList: urlList,
		})
	}

	inputText = ReplaceSourcesWithUrls(inputText, urlList)

	for i, _ := range blocks {
		inputText = strings.Replace(inputText, "///TEXT_BLOCK_PLACEHOLDER"+fmt.Sprint(i)+"///", "", 1)
	}

	inputText = strings.ReplaceAll(inputText, "%NEW_LINE%", "\n")
	return inputText
}