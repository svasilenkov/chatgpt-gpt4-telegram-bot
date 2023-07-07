package main

import (
	"fmt"
	"regexp"
	"strconv"
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
			text = regexp.MustCompile(match).ReplaceAllString(text, " ["+fmt.Sprint(index+1)+"]("+urls[index]+")")
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
			//text = regexp.MustCompile(`click\(`+num+`\)`).ReplaceAllString(text, `click("[`+urls[numInt]+`](`+urls[numInt]+`)")`)
			text = regexp.MustCompile(`click\(`+num+`\)`).ReplaceAllString(text, `click("`+urls[numInt]+`")`)
		} else {
			fmt.Println("Number in 'click' is out of URLs array bounds")
		}
	}

	return text
}
