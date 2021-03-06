require 'socket'
require 'timeout'
require 'bosh/dev'

module Bosh::Dev::Sandbox
  class SocketConnector
    def initialize(host, port, logger)
      @host = host
      @port = port
      @logger = logger
    end

    def try_to_connect(remaining_attempts = 20)
      remaining_attempts -= 1
      Timeout.timeout(1) { TCPSocket.new(@host, @port).close }
    rescue Timeout::Error, Errno::ECONNREFUSED, Errno::EHOSTUNREACH, Errno::ETIMEDOUT
      raise if remaining_attempts == 0
      sleep(0.2) # unfortunate fine-tuning required here
      retry
    end
  end
end
