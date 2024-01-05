// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: MIT-0
import {
  Box,
  Container,
  Header,
  HelpPanel,
  Icon,
} from "@cloudscape-design/components";
import {
  CounterLink,
  ExternalLinkItem,
  InfoLink,
} from "../../../commons/common-components";
import { keepThreeDecimal } from "../components-common";

function TotalComputeCostInfo() {
  return (
    <HelpPanel
      header={<h2>All Compute Spend</h2>}
      footer={
        <>
          <h3>
            Learn more{" "}
            <span role="img" aria-label="Icon external Link">
              <Icon name="external" />
            </span>
          </h3>
          <ul>
            <li>
              <ExternalLinkItem href="#" text="All compute spend" />
            </li>
            <li>
              <ExternalLinkItem href="#" text="cluster spend metrics" />
            </li>
          </ul>
        </>
      }
    >
      <p>
        All costs of your clusters
      </p>
    </HelpPanel>
  );
}

export default function ClustersCostOverview(props) {
  return (
    <Container
      className="custom-dashboard-container"
      header={
        <Header
          variant="h2"
          description="Sum of all current clusters configuration costs"
          info={
            <InfoLink
              onFollow={() => props.updateTools(<TotalComputeCostInfo />)}
              ariaLabel={"Sum of all current clusters configuration costs."}
            />
          }
        >
          All compute spend
        </Header>
      }
    >
      <Box textAlign="right" margin={{ top: "xl" }}>
        <CounterLink>${keepThreeDecimal(props.totalCost)}</CounterLink>
        <Box color="text-body-secondary">Month</Box>
      </Box>
    </Container>
  );
}
