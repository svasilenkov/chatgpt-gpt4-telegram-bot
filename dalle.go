package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
)

type GenerationResultData struct {
	Url string `json:"url"`
}

type GenerationsResult struct {
	Created int                    `json:"created"`
	Data    []GenerationResultData `json:"data"`
}

func DalleGenerations(openaiToken, prompt string, imageCount int, size string) GenerationsResult {
	result := SendJSONToURLBearer(
		"https://api.openai.com/v1/images/generations",
		`{ "model": "`+DalleModel+`",  "prompt": "`+prompt+`", "n":`+fmt.Sprint(imageCount)+`, "size":"`+size+`" }`, openaiToken, "POST")

	var generationsResult GenerationsResult
	err := json.Unmarshal(result, &generationsResult)
	if err != nil {
		fmt.Println(err)
		return GenerationsResult{}
	}
	return generationsResult
}

func SendJSONToURLBearer(url string, jsons string, token, method string) []byte {
	return SendJSONToURLBearer2(url, jsons, token, method, make(map[string]string))
}

func SendJSONToURLBearer2(url string, jsons string, token string, method string, header map[string]string) []byte {
	// Create a Bearer string by appending string access token
	var bearer = "Bearer " + token

	// Create a new request using http
	req, err := http.NewRequest(method, url, bytes.NewBuffer([]byte(jsons)))

	// add authorization header to the req
	req.Header.Add("Authorization", bearer)
	req.Header.Set("Content-Type", "application/json")

	for key, value := range header {
		req.Header.Add(key, value)
	}

	// Send req using http Client
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		fmt.Println("Error on response.\n[ERRO] -", err)
	}

	body, _ := ioutil.ReadAll(resp.Body)
	return []byte(body)
}
