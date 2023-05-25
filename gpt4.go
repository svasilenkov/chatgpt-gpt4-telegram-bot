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
	if len(urls) == 0 {
		return text
	}
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

func GPT4BrowsingReplaceMetadata(inputText string, cleanCodeBlocks bool) string {
	if strings.Count(inputText, "///text_message") != strings.Count(inputText, "\n"+`%%%TEXT_METADATA:`) {
		inputText += "\n" + `%%%TEXT_METADATA:{}%%%` + "\n" + `\\\`
	}
	if strings.Count(inputText, "///") != strings.Count(inputText, "\n"+`\\\`) {
		inputText += "\n" + `\\\`
	}

	inputText = strings.ReplaceAll(inputText, "\n", "%NEW_LINE%")

	// Find all blocks with CODE_METADATA
	re := regexp.MustCompile(`\/\/\/` + "code_message%NEW_LINE%%%%METADATA:(.+?)%%%(.*?)%NEW_LINE%" + `\\\\\\`)
	blockMatches := re.FindAllStringSubmatch(inputText, -1)

	// Parse each block's CODE_METADATA
	for _, blockMatch := range blockMatches {
		metadataString := blockMatch[1]
		var urlList []string
		var metadata Metadata
		err := json.Unmarshal([]byte(metadataString), &metadata)
		if err != nil {
			//panic(err)
		}
		for _, item := range metadata.CiteMetadata.MetaDataList {
			urlList = append(urlList, item.Url)
		}
		text := ReplaceClickWithUrl(blockMatch[2], urlList)
		if cleanCodeBlocks || strings.Contains(text, "quote(") {
			inputText = strings.Replace(inputText, blockMatch[0], "", 1)
		} else {
			inputText = strings.Replace(inputText, blockMatch[0], text+"\n", 1)
		}
	}

	re = regexp.MustCompile(`\/\/\/` + "text_message%NEW_LINE%(.*?)%%%TEXT_METADATA:(.+?)%%%%NEW_LINE%" + `\\\\\\`)
	blockMatches = re.FindAllStringSubmatch(inputText, -1)

	// Parse each block's TEXT_METADATA
	for _, blockMatch := range blockMatches {
		metadataString := blockMatch[2]

		var metadata Metadata
		err := json.Unmarshal([]byte(metadataString), &metadata)
		if err != nil {
			//fmt.Println(metadataString)
			//panic(err)
		}
		var urlList []string
		for _, item := range metadata.Citations {
			urlList = append(urlList, item.Metadata.Url)
		}
		text := ReplaceSourcesWithUrls(blockMatch[1], urlList)
		inputText = strings.Replace(inputText, blockMatch[0], text+"\n", 1)
	}

	inputText = strings.ReplaceAll(inputText, "%NEW_LINE%", "\n")
	return inputText
}
