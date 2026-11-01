package cmd

import (
	"errors"

	"github.com/spf13/cobra"
)

func (a *app) domainCommands() []*cobra.Command {
	user := &cobra.Command{Use: "user", Short: "Read user information"}
	user.AddCommand(a.simple("me", "Read the current user", "get_me", ""), a.simple("get USER_ID", "Read a user by ID", "get_user", "user_id"))
	team := &cobra.Command{Use: "team", Short: "Read teams"}
	team.AddCommand(a.simple("list", "List the current user's teams", "get_user_teams", ""), a.simple("get NAME", "Find a team by name", "get_team_info", "team_name"))
	channel := &cobra.Command{Use: "channel", Short: "Read channels and recent messages"}
	channel.AddCommand(a.simple("get CHANNEL_ID", "Read channel information", "get_channel_info", "channel_id"), a.simple("list TEAM_ID", "List channels in a team", "list_team_channels", "team_id"))
	var limit int
	read := &cobra.Command{Use: "read CHANNEL_ID", Short: "Read recent messages", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if limit <= 0 || limit > 100 {
			return errors.New("limit must be between 1 and 100")
		}
		return a.call(cmd, "read_channel", map[string]any{"channel_id": args[0], "limit": limit})
	}}
	read.Flags().IntVar(&limit, "limit", 20, "Maximum messages to request")
	channel.AddCommand(read)
	post := &cobra.Command{Use: "post", Short: "Read, create and reply to messages"}
	post.AddCommand(a.simple("get POST_ID", "Read a post and its thread", "read_post", "post_id"), a.messageCommand())
	var teamID, channelID string
	search := &cobra.Command{Use: "search QUERY", Short: "Search messages using Mattermost search syntax", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		params := map[string]any{"query": args[0]}
		if teamID != "" {
			params["team_id"] = teamID
		}
		if channelID != "" {
			params["channel_id"] = channelID
		}
		return a.call(cmd, "search_posts", params)
	}}
	search.Flags().StringVar(&teamID, "team-id", "", "Restrict search to a team")
	search.Flags().StringVar(&channelID, "channel-id", "", "Restrict search to a channel")
	return []*cobra.Command{user, team, channel, post, search}
}

func (a *app) simple(use, short, tool, parameter string) *cobra.Command {
	validator := cobra.NoArgs
	if parameter != "" {
		validator = cobra.ExactArgs(1)
	}
	return &cobra.Command{Use: use, Short: short, Args: validator, RunE: func(cmd *cobra.Command, args []string) error {
		params := map[string]any{}
		if parameter != "" {
			params[parameter] = args[0]
		}
		return a.call(cmd, tool, params)
	}}
}

func (a *app) messageCommand() *cobra.Command {
	var message, file, rootID, channelName, teamName string
	command := &cobra.Command{Use: "create CHANNEL_ID", Short: "Create a message, or reply with --root-id", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if file != "" && cmd.Flags().Changed("message") {
			return errors.New("use either --message or --message-file")
		}
		if file == "-" && a.passwordStdin {
			return errors.New("stdin cannot supply both password and message")
		}
		body := message
		if file != "" {
			data, err := readFile(cmd, file)
			if err != nil {
				return err
			}
			body = string(data)
		}
		if body == "" {
			return errors.New("supply --message or --message-file")
		}
		params := map[string]any{"channel_id": args[0], "message": body}
		if channelName != "" {
			params["channel_display_name"] = channelName
		}
		if teamName != "" {
			params["team_display_name"] = teamName
		}
		if rootID != "" {
			params["root_id"] = rootID
		}
		return a.call(cmd, "create_post", params)
	}}
	command.Flags().StringVarP(&message, "message", "m", "", "Message text")
	command.Flags().StringVar(&file, "message-file", "", "Read message from a UTF-8 file, or - for stdin")
	command.Flags().StringVar(&rootID, "root-id", "", "Root post ID when replying to a thread")
	command.Flags().StringVar(&channelName, "channel-display-name", "", "Channel display name for context verification (required by newer servers)")
	command.Flags().StringVar(&teamName, "team-display-name", "", "Team display name for public/private channel context verification")
	return command
}
