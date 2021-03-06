#pragma once

#include <gmock/gmock.h>

#include "container_reader.hpp"

class MockContainerReader : public metrics::ContainerReader {
 public:
  MOCK_METHOD0(open, Try<metrics::UDPEndpoint>());
  MOCK_METHOD0(close, void());
  MOCK_CONST_METHOD0(endpoint, Try<metrics::UDPEndpoint>());
  MOCK_METHOD2(register_container, void(
          const mesos::ContainerID& container_id, const mesos::ExecutorInfo& executor_info));
  MOCK_METHOD1(unregister_container, void(const mesos::ContainerID& container_id));
};
