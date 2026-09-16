terraform {
  required_providers {
    cursor = {
      source  = "cursor/cursor"
      version = "~> 0.1"
    }
  }
}

provider "cursor" {
  # Or set the CURSOR_TOKEN environment variable.
  token = var.cursor_token

  # Only needed for Origin grants keyed by user_email or group_name.
  # Or set CURSOR_TEAM_API_KEY and CURSOR_ORGANIZATION_API_KEY.
  team_api_key         = var.cursor_team_api_key
  organization_api_key = var.cursor_organization_api_key
}
