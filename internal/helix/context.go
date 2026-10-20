package helix

import (
	"context"
	"fmt"
	"net/url"
)

type ChatSettings struct {
	FollowerMode    bool `json:"follower_mode"`
	FollowerMinutes *int `json:"follower_mode_duration"`
	SubscriberMode  bool `json:"subscriber_mode"`
	EmoteMode       bool `json:"emote_mode"`
	SlowMode        bool `json:"slow_mode"`
	SlowSeconds     *int `json:"slow_mode_wait_time"`
	UniqueMode      bool `json:"unique_chat_mode"`
}

func (c *Client) ChatSettings(ctx context.Context, id string) (ChatSettings, error) {
	var r struct {
		Data []ChatSettings `json:"data"`
	}
	err := c.get(ctx, PriorityLow, "/chat/settings", url.Values{"broadcaster_id": {id}}, &r)
	if err != nil {
		return ChatSettings{}, err
	}
	if len(r.Data) != 1 {
		return ChatSettings{}, fmt.Errorf("chat settings: expected one row")
	}
	return r.Data[0], nil
}

type SharedSession struct {
	ID           string `json:"session_id"`
	Host         string `json:"host_broadcaster_id"`
	Participants []struct {
		ID string `json:"broadcaster_id"`
	} `json:"participants"`
}

func (c *Client) SharedChat(ctx context.Context, id string) (*SharedSession, error) {
	var r struct {
		Data []SharedSession `json:"data"`
	}
	if err := c.get(ctx, PriorityLow, "/shared_chat/session", url.Values{"broadcaster_id": {id}}, &r); err != nil {
		return nil, err
	}
	if len(r.Data) == 0 {
		return nil, nil
	}
	return &r.Data[0], nil
}
