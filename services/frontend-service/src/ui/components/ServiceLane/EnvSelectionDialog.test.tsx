/*This file is part of kuberpult.

Kuberpult is free software: you can redistribute it and/or modify
it under the terms of the Expat(MIT) License as published by
the Free Software Foundation.

Kuberpult is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
MIT License for more details.

You should have received a copy of the MIT License
along with kuberpult. If not, see <https://directory.fsf.org/wiki/License:Expat>.

Copyright 2023 freiheit.com*/
import { act, render } from '@testing-library/react';
import { documentQuerySelectorSafe } from '../../../setupTests';
import { EnvSelectionDialog, EnvSelectionDialogProps } from './EnvSelectionDialog';

type TestDataSelection = {
    name: string;
    input: EnvSelectionDialogProps;
    expectedNumItems: number;
    clickOnButton: string;
    expectedNumSelectedAfterClick: number;
    expectedNumDeselectedAfterClick: number;
};

const mySubmitSpy = jest.fn();
const myCancelSpy = jest.fn();

const confirmButtonSelector = '.test-button-confirm';

const dataSelection: TestDataSelection[] = [
    {
        name: 'renders 2 item list',
        input: { environments: ['dev', 'staging'], open: true, onSubmit: mySubmitSpy, onCancel: myCancelSpy },
        expectedNumItems: 2,
        clickOnButton: 'dev',
        expectedNumSelectedAfterClick: 1,
        expectedNumDeselectedAfterClick: 1,
    },
    {
        name: 'renders 3 item list',
        input: { environments: ['dev', 'staging', 'prod'], open: true, onSubmit: mySubmitSpy, onCancel: myCancelSpy },
        expectedNumItems: 3,
        clickOnButton: 'staging',
        expectedNumSelectedAfterClick: 1,
        expectedNumDeselectedAfterClick: 2,
    },
];

type TestDataOpenClose = {
    name: string;
    input: EnvSelectionDialogProps;
    expectedNumElements: number;
};
const dataOpenClose: TestDataOpenClose[] = [
    {
        name: 'renders open dialog',
        input: {
            environments: ['dev', 'staging', 'prod'],
            open: true,
            onSubmit: mySubmitSpy,
            onCancel: myCancelSpy,
        },
        expectedNumElements: 1,
    },
    {
        name: 'renders closed dialog',
        input: {
            environments: ['dev', 'staging', 'prod'],
            open: false,
            onSubmit: mySubmitSpy,
            onCancel: myCancelSpy,
        },
        expectedNumElements: 0,
    },
];

type TestDataCallbacks = {
    name: string;
    input: EnvSelectionDialogProps;
    clickThis: string;
    expectedCancelCallCount: number;
    expectedSubmitCallCount: number;
};
const dataCallbacks: TestDataCallbacks[] = [
    {
        name: 'renders open dialog',
        input: {
            environments: ['dev', 'staging', 'prod'],
            open: true,
            onSubmit: mySubmitSpy,
            onCancel: myCancelSpy,
        },
        clickThis: '.test-button-cancel',
        expectedCancelCallCount: 1,
        expectedSubmitCallCount: 0,
    },
    {
        name: 'renders closed dialog',
        input: {
            environments: ['dev', 'staging', 'prod'],
            open: true,
            onSubmit: mySubmitSpy,
            onCancel: myCancelSpy,
        },
        clickThis: confirmButtonSelector,
        expectedCancelCallCount: 0,
        expectedSubmitCallCount: 1,
    },
];

const getNode = (overrides: EnvSelectionDialogProps) => <EnvSelectionDialog {...overrides} />;
const getWrapper = (overrides: EnvSelectionDialogProps) => render(getNode(overrides));

describe('EnvSelectionDialog', () => {
    describe.each(dataSelection)('Test checkbox enabled', (testcase) => {
        it(testcase.name, () => {
            mySubmitSpy.mockReset();
            myCancelSpy.mockReset();
            expect(mySubmitSpy).toHaveBeenCalledTimes(0);
            expect(myCancelSpy).toHaveBeenCalledTimes(0);

            getWrapper(testcase.input);

            expect(document.querySelectorAll('.envs-dropdown-select .test-button-checkbox').length).toEqual(
                testcase.expectedNumItems
            );
            const result = documentQuerySelectorSafe('.id-' + testcase.clickOnButton);
            act(() => {
                result.click();
            });
            expect(document.querySelectorAll('.test-button-checkbox.enabled').length).toEqual(
                testcase.expectedNumSelectedAfterClick
            );
            expect(document.querySelectorAll('.test-button-checkbox.disabled').length).toEqual(
                testcase.expectedNumDeselectedAfterClick
            );
        });
    });
    describe.each(dataOpenClose)('open/close', (testcase) => {
        it(testcase.name, () => {
            getWrapper(testcase.input);
            expect(document.querySelectorAll('.envs-dropdown-select').length).toEqual(testcase.expectedNumElements);
        });
    });
    describe.each(dataCallbacks)('submit/cancel callbacks', (testcase) => {
        it(testcase.name, () => {
            mySubmitSpy.mockReset();
            myCancelSpy.mockReset();
            expect(mySubmitSpy).toHaveBeenCalledTimes(0);
            expect(myCancelSpy).toHaveBeenCalledTimes(0);

            getWrapper(testcase.input);

            const theButton = documentQuerySelectorSafe(testcase.clickThis);
            act(() => {
                theButton.click();
            });
            documentQuerySelectorSafe(testcase.clickThis); // should not crash

            expect(myCancelSpy).toHaveBeenCalledTimes(testcase.expectedCancelCallCount);
            expect(mySubmitSpy).toHaveBeenCalledTimes(testcase.expectedSubmitCallCount);
        });
    });

    type TestDataAddTeam = {
        name: string;
        input: EnvSelectionDialogProps;
        clickTheseTeams: string[];
        expectedCancelCallCount: number;
        expectedSubmitCallCount: number;
        expectedSubmitCalledWith: string[];
    };
    const addTeamArray: TestDataAddTeam[] = [
        {
            name: '1 env',
            input: {
                environments: ['dev', 'staging', 'prod'],
                open: true,
                onSubmit: mySubmitSpy,
                onCancel: myCancelSpy,
            },
            clickTheseTeams: ['dev'],
            expectedCancelCallCount: 0,
            expectedSubmitCallCount: 1,
            expectedSubmitCalledWith: ['dev'],
        },
        {
            name: '2 envs',
            input: {
                environments: ['dev', 'staging', 'prod'],
                open: true,
                onSubmit: mySubmitSpy,
                onCancel: myCancelSpy,
            },
            clickTheseTeams: ['staging', 'prod'],
            expectedCancelCallCount: 0,
            expectedSubmitCallCount: 1,
            expectedSubmitCalledWith: ['staging', 'prod'],
        },
        {
            name: '1 env clicked twice',
            input: {
                environments: ['dev', 'staging', 'prod'],
                open: true,
                onSubmit: mySubmitSpy,
                onCancel: myCancelSpy,
            },
            clickTheseTeams: ['dev', 'staging', 'staging'],
            expectedCancelCallCount: 0,
            expectedSubmitCallCount: 1,
            expectedSubmitCalledWith: ['dev'],
        },
    ];
    describe.each(addTeamArray)('adding 2 teams works', (testcase) => {
        it(testcase.name, () => {
            mySubmitSpy.mockReset();
            myCancelSpy.mockReset();
            expect(mySubmitSpy).toHaveBeenCalledTimes(0);
            expect(myCancelSpy).toHaveBeenCalledTimes(0);

            getWrapper(testcase.input);

            testcase.clickTheseTeams.forEach((value, index) => {
                const teamButton = documentQuerySelectorSafe('.id-' + value);
                act(() => {
                    teamButton.click();
                });
            });
            const confirmButton = documentQuerySelectorSafe(confirmButtonSelector);
            act(() => {
                confirmButton.click();
            });

            expect(myCancelSpy).toHaveBeenCalledTimes(testcase.expectedCancelCallCount);
            expect(mySubmitSpy).toHaveBeenCalledTimes(testcase.expectedSubmitCallCount);
            expect(mySubmitSpy).toHaveBeenCalledWith(testcase.expectedSubmitCalledWith);
        });
    });
});
