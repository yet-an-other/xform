// conflictText renders the mutation API's machine-readable rejection
// reasons for the add and edit dialogs (user-management spec §5).

export function conflictText(reason: string): string {
  switch (reason) {
    case "email_taken":
      return "That email is already in the roster.";
    case "email_invalid":
      return "Enter an email address.";
    case "client_id_taken":
      return "That Client ID is already used by another user.";
    case "client_id_invalid":
      return "Client ID must be a valid UUID.";
    case "unknown_inbound":
      return "An attached inbound no longer exists — close and reopen to refresh.";
    default:
      return "The panel rejected the change.";
  }
}
