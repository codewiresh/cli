# Homebrew Formula for CodeWire
class Codewire < Formula
  desc "Persistent process server for AI coding agents"
  homepage "https://github.com/codewiresh/codewire"
  version "0.2.48"
  license "MIT"

  on_macos do
    if Hardware::CPU.arm?
      url "https://github.com/codewiresh/codewire/releases/download/v#{version}/cw-v#{version}-aarch64-apple-darwin"
      sha256 "87583830c8fa23a1a664aa2eae5c1c1cbec87f5dc3779ce5b22613229444dbed"
    else
      url "https://github.com/codewiresh/codewire/releases/download/v#{version}/cw-v#{version}-x86_64-apple-darwin"
      sha256 "61d6f0d4d11b05f1b62e705d99c161a5274bd4d2b7a05272bab6cf26abc7d235"
    end
  end

  on_linux do
    if Hardware::CPU.arm?
      url "https://github.com/codewiresh/codewire/releases/download/v#{version}/cw-v#{version}-aarch64-unknown-linux-gnu"
      sha256 "4e6cb420c733bc8406f1f63d6b796149bf4fa59b9d376badd8f5e0d21b71b2b2"
    else
      url "https://github.com/codewiresh/codewire/releases/download/v#{version}/cw-v#{version}-x86_64-unknown-linux-musl"
      sha256 "c85f2a5812640d27576def0453819389880ceaf283f2ff0a712f51625a126e04"
    end
  end

  def install
    # Determine the correct binary name based on platform
    if OS.mac?
      if Hardware::CPU.arm?
        binary_name = "cw-v#{version}-aarch64-apple-darwin"
      else
        binary_name = "cw-v#{version}-x86_64-apple-darwin"
      end
    else
      if Hardware::CPU.arm?
        binary_name = "cw-v#{version}-aarch64-unknown-linux-gnu"
      else
        binary_name = "cw-v#{version}-x86_64-unknown-linux-musl"
      end
    end

    bin.install binary_name => "cw"
    generate_completions_from_executable(bin/"cw", "completion")
  end

  test do
    # Test that the binary runs and shows help
    assert_match "Persistent process server", shell_output("#{bin}/cw --help")

    # Test version display
    system "#{bin}/cw", "--version"
  end

  def caveats
    <<~EOS
      CodeWire node will auto-start on first command.

      Quick start:
        cw launch -- claude -p "your prompt here"
        cw list
        cw attach 1

      For more information:
        https://github.com/codewiresh/codewire
    EOS
  end
end
