# Homebrew Formula for CodeWire
class Codewire < Formula
  desc "Persistent process server for AI coding agents"
  homepage "https://github.com/codewiresh/codewire"
  version "0.2.47"
  license "MIT"

  on_macos do
    if Hardware::CPU.arm?
      url "https://github.com/codewiresh/codewire/releases/download/v#{version}/cw-v#{version}-aarch64-apple-darwin"
      sha256 "d9b989968723726dae5fcc7fa8152ef8b35b3207b7a618614a6c0105798eca4d"
    else
      url "https://github.com/codewiresh/codewire/releases/download/v#{version}/cw-v#{version}-x86_64-apple-darwin"
      sha256 "34e48cc17890ab15192eaecbf260cb1ecdd0f8e1ff2c71666271bfa6ed3d80f8"
    end
  end

  on_linux do
    if Hardware::CPU.arm?
      url "https://github.com/codewiresh/codewire/releases/download/v#{version}/cw-v#{version}-aarch64-unknown-linux-gnu"
      sha256 "571a9865e6606463aa16474677f168d71ce53677d1755150ae3b37ad9d9569a9"
    else
      url "https://github.com/codewiresh/codewire/releases/download/v#{version}/cw-v#{version}-x86_64-unknown-linux-musl"
      sha256 "745e61721b883f0cd0b6bb4a4a927f868285b92b4a098b383b679be270dacdcf"
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
