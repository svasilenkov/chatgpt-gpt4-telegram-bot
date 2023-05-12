package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"log"
	"math/rand"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

type BardChatbot struct {
	sessionId       string
	sessionAt       string
	headers         http.Header
	reqid           int
	conversation_id string
	response_id     string
	choice_id       string
}

func BardNewChatbot(sessionId string) *BardChatbot {
	rand.Seed(time.Now().UnixNano())

	sessionAt, err := getSessionAt(sessionId)
	if err != nil {
		return nil
	}

	return &BardChatbot{
		sessionId: sessionId,
		sessionAt: sessionAt,
		headers: http.Header{
			"Host":          []string{"bard.google.com"},
			"X-Same-Domain": []string{"1"},
			"User-Agent":    []string{"Mozilla/5.0 (Windows NT 10.0; WOW64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/91.0.4472.114 Safari/537.36"},
			"Content-Type":  []string{"application/x-www-form-urlencoded;charset=UTF-8"},
			"Origin":        []string{"https://bard.google.com"},
			"Referer":       []string{"https://bard.google.com/"},
			"Cookie":        []string{fmt.Sprintf("__Secure-1PSID=%s", sessionId)},
		},
		reqid: rand.Intn(10000),
	}
}

func getSessionAt(sessionId string) (string, error) {
	req, err := http.NewRequest("GET", "https://bard.google.com/", nil)
	if err != nil {
		return "", err
	}
	req.Header = http.Header{
		"Host":          []string{"bard.google.com"},
		"X-Same-Domain": []string{"1"},
		"User-Agent":    []string{"Mozilla/5.0 (Windows NT 10.0; WOW64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/91.0.4472.114 Safari/537.36"},
		"Content-Type":  []string{"application/x-www-form-urlencoded;charset=UTF-8"},
		"Origin":        []string{"https://bard.google.com"},
		"Referer":       []string{"https://bard.google.com/"},
		"Cookie":        []string{fmt.Sprintf("__Secure-1PSID=%s", sessionId)},
	}

	// Send the request
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("Error: %d", resp.StatusCode)
	}

	content, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	match := regexp.MustCompile(`SNlM0e":"(.*?)"`).FindStringSubmatch(string(content))
	if len(match) != 2 {
		fmt.Println(string(content))
		return "", errors.New("Could not find SNlM0e")
	}

	return match[1], nil
}

func (c *BardChatbot) Ask(message string) (string, error) {
	messageStruct := [][]interface{}{
		{message},
		nil,
		{c.conversation_id, c.response_id, c.choice_id},
	}

	messageStructJson, _ := json.Marshal(messageStruct)

	params := map[string]string{
		"f.req": `[null, "` + strings.ReplaceAll(string(messageStructJson), `"`, `\"`) + `"]`,
		"at":    c.sessionAt,
	}

	values := make(url.Values)

	for key, val := range params {
		values.Add(key, val)
	}

	// Make the request
	req, err := http.NewRequest("POST", "https://bard.google.com/_/BardChatUi/data/assistant.lamda.BardFrontendService/StreamGenerate?bl="+url.QueryEscape("boq_assistant-bard-web-server_20230419.00_p1")+"&_reqid="+fmt.Sprintf("%04d", c.reqid)+"&rt=c", strings.NewReader(values.Encode()))
	if err != nil {
		return "", err
	}
	req.Header = c.headers

	// Send the request
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("Error: %d", resp.StatusCode)
	}

	// Read the response body
	content, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	log.Printf("Bard AI response: %s", content)

	// Split the response body
	split := strings.Split(string(content), "\n")
	if len(split) < 4 {
		return "", fmt.Errorf("Error: invalid response")
	}
	wholeResponse := [][]interface{}{}

	// Parse the response body
	if err := json.Unmarshal([]byte(split[3]), &wholeResponse); err != nil {
		return "", err
	}

	responseData := [][]interface{}{}

	if len(wholeResponse) < 1 || len(wholeResponse[0]) < 3 {
		return "", fmt.Errorf("Error: invalid response")
	}
	if wholeResponse[0][2] == nil {
		return "", fmt.Errorf("Error: invalid response")
	}

	if err := json.Unmarshal([]byte(wholeResponse[0][2].(string)), &responseData); err != nil {
		return "", err
	}
	choices := []interface{}{}
	if len(responseData) < 5 || len(responseData[4]) < 1 {
		return "", fmt.Errorf("Error: invalid response")
	}
	choices = responseData[4][0].([]interface{})
	if len(choices) < 1 {
		return "", fmt.Errorf("Error: invalid response")
	}
	c.conversation_id = responseData[1][0].(string)
	c.response_id = responseData[1][1].(string)
	c.choice_id = choices[0].(string)
	c.reqid += 100000

	return responseData[0][0].(string), nil
}

func (c *BardChatbot) Reset() {
	c.conversation_id = ""
	c.response_id = ""
	c.choice_id = ""
}
