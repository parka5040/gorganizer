package protontricks

type NotInstalledError struct{}

// Error describes how to install Protontricks when no supported installation is found.
func (*NotInstalledError) Error() string {
	return "Protontricks is not installed. Gorganizer needs it to add Windows components (such as Visual C++ and DirectX runtimes) to the game's Proton prefix. Install Protontricks from your software centre or Flathub, then try again."
}
