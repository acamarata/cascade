cask "cascade" do
  version "1.16.0"
  sha256 :no_check # placeholder; updated by .github/workflows/update-homebrew.yml on each release

  url "https://github.com/acamarata/cascade/releases/download/v#{version}/Cascade_#{version}_macos_universal.dmg"

  name "Cascade"
  desc "Unified AI instruction cascade manager"
  homepage "https://github.com/acamarata/cascade"

  depends_on macos: ">= :monterey"

  app "Cascade.app"

  binary "#{appdir}/Cascade.app/Contents/MacOS/cascade"

  zap trash: [
    "~/.cascade/",
    "~/Library/Application Support/dev.cascade.app/",
    "~/Library/LaunchAgents/dev.cascade.daemon.plist",
    "~/Library/Logs/cascade-*.log",
    "~/Library/Preferences/dev.cascade.app.plist",
  ]
end
