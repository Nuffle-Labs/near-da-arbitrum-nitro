// SPDX-License-Identifier: Apache-2.0

/*
 * Copyright 2021, Offchain Labs, Inc.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *    http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

pragma solidity ^0.8.0;

import "@openzeppelin/contracts/proxy/ERC1967/ERC1967Proxy.sol";
import "@openzeppelin/contracts/proxy/ERC1967/ERC1967Upgrade.sol";
import "@openzeppelin/contracts/proxy/utils/UUPSUpgradeable.sol";
import "@openzeppelin/contracts/utils/Address.sol";
import "@openzeppelin/contracts/utils/StorageSlot.sol";


/// @notice An extension to OZ's ERC1967Upgrade implementation to support two logic contracts
abstract contract DoubleLogicERC1967Upgrade is ERC1967Upgrade {
    using Address for address;

    // This is the keccak-256 hash of "eip1967.proxy.implementation.secondary" subtracted by 1
    bytes32 internal constant _IMPLEMENTATION_SECONDARY_SLOT = 0x2b1dbce74324248c222f0ec2d5ed7bd323cfc425b336f0253c5ccfda7265546d;

    // This is the keccak-256 hash of "eip1967.proxy.rollback.secondary" subtracted by 1
    bytes32 private constant _ROLLBACK_SECONDARY_SLOT = 0x49bd798cd84788856140a4cd5030756b4d08a9e4d55db725ec195f232d262a89;

    /**
     * @dev Emitted when the secondary implementation is upgraded.
     */
    event UpgradedSecondary(address indexed implementation);

    /**
     * @dev Returns the current secondary implementation address.
     */
    function _getSecondaryImplementation() internal view returns (address) {
        return StorageSlot.getAddressSlot(_IMPLEMENTATION_SECONDARY_SLOT).value;
    }

    /**
     * @dev Stores a new address in the EIP1967 implementation slot.
     */
    function _setSecondaryImplementation(address newImplementation) private {
        require(Address.isContract(newImplementation), "ERC1967: new secondary implementation is not a contract");
        StorageSlot.getAddressSlot(_IMPLEMENTATION_SECONDARY_SLOT).value = newImplementation;
    }

    /**
     * @dev Perform secondary implementation upgrade
     *
     * Emits an {UpgradedSecondary} event.
     */
    function _upgradeSecondaryTo(address newImplementation) internal {
        _setSecondaryImplementation(newImplementation);
        emit UpgradedSecondary(newImplementation);
    }

    /**
     * @dev Perform secondary implementation upgrade with additional setup call.
     *
     * Emits an {UpgradedSecondary} event.
     */
    function _upgradeSecondaryToAndCall(
        address newImplementation,
        bytes memory data,
        bool forceCall
    ) internal {
        _upgradeSecondaryTo(newImplementation);
        if (data.length > 0 || forceCall) {
            Address.functionDelegateCall(newImplementation, data);
        }
    }

    /**
     * @dev Perform secondary implementation upgrade with security checks for UUPS proxies, and additional setup call.
     *
     * Emits an {UpgradedSecondary} event.
     */
    function _upgradeSecondaryToAndCallUUPS(
        address newImplementation,
        bytes memory data,
        bool forceCall
    ) internal {
        // Upgrades from old implementations will perform a rollback test. This test requires the new
        // implementation to upgrade back to the old, non-ERC1822 compliant, implementation. Removing
        // this special case will break upgrade paths from old UUPS implementation to new ones.
        if (StorageSlot.getBooleanSlot(_ROLLBACK_SECONDARY_SLOT).value) {
            _setSecondaryImplementation(newImplementation);
        } else {
            try IERC1822Proxiable(newImplementation).proxiableUUID() returns (bytes32 slot) {
                require(slot == _IMPLEMENTATION_SECONDARY_SLOT, "ERC1967Upgrade: unsupported proxiableUUID");
            } catch {
                revert("ERC1967Upgrade: new secondary implementation is not UUPS");
            }
            _upgradeSecondaryToAndCall(newImplementation, data, forceCall);
        }
    }
}


/// @notice An extension to OZ's UUPSUpgradeable contract to be used in secondary logic contracts from DoubleLogicERC1967Upgrade
abstract contract SecondaryLogicUUPSUpgradeable is UUPSUpgradeable, DoubleLogicERC1967Upgrade {
    /// @inheritdoc UUPSUpgradeable
    function proxiableUUID() external view override notDelegated returns (bytes32) {
        return _IMPLEMENTATION_SECONDARY_SLOT;
    }

    /**
     * @dev Function that should revert when `msg.sender` is not authorized to upgrade the secondary contract. Called by
     * {upgradeSecondaryTo} and {upgradeSecondaryToAndCall}.
     *
     * Normally, this function will use an xref:access.adoc[access control] modifier such as {Ownable-onlyOwner}.
     *
     * ```solidity
     * function _authorizeSecondaryUpgrade(address) internal override onlyOwner {}
     * ```
     */
    function _authorizeSecondaryUpgrade(address newImplementation) internal virtual;

    /**
     * @dev Upgrade the secondary implementation of the proxy to `newImplementation`.
     *
     * Calls {_authorizeSecondaryUpgrade}.
     *
     * Emits an {UpgradedSecondary} event.
     */
    function upgradeSecondaryTo(address newImplementation) external onlyProxy {
        _authorizeSecondaryUpgrade(newImplementation);
        _upgradeSecondaryToAndCallUUPS(newImplementation, new bytes(0), false);
    }

    /**
     * @dev Upgrade the secondary implementation of the proxy to `newImplementation`, and subsequently execute the function call
     * encoded in `data`.
     *
     * Calls {_authorizeSecondaryUpgrade}.
     *
     * Emits an {UpgradedSecondary} event.
     */
    function upgradeSecondaryToAndCall(address newImplementation, bytes memory data) external payable onlyProxy {
        _authorizeSecondaryUpgrade(newImplementation);
        _upgradeSecondaryToAndCallUUPS(newImplementation, data, true);
    }
}


/// @notice similar to TransparentUpgradeableProxy but allows the admin to fallback to a separate logic contract using DoubleLogicERC1967Proxy
/// @dev this follows the UUPS pattern for upgradeability - read more at https://github.com/OpenZeppelin/openzeppelin-contracts/tree/v4.5.0/contracts/proxy#transparent-vs-uups-proxies
contract AdminFallbackProxy is ERC1967Proxy, DoubleLogicERC1967Upgrade {

    /**
     * @dev Initializes the upgradeable proxy with an initial implementation specified by `userLogic` and a secondary
     * logic implementation specified by `adminLogic`
     *
     * Only the `adminAddr` is able to use the `adminLogic` functions
     */
    constructor(
        address userLogic,
        bytes memory userData,
        address adminLogic,
        bytes memory adminData,
        address adminAddr
    ) payable ERC1967Proxy(userLogic, userData) {
         assert(_ADMIN_SLOT == bytes32(uint256(keccak256("eip1967.proxy.admin")) - 1));
         assert(_IMPLEMENTATION_SECONDARY_SLOT == bytes32(uint256(keccak256("eip1967.proxy.implementation.secondary")) - 1));
        _changeAdmin(adminAddr);
        _upgradeSecondaryToAndCall(adminLogic, adminData, false);
    }

    /**
     * @dev Returns the current secondary implementation address.
     */
    function _secondaryImplementation() internal view returns (address impl) {
        return DoubleLogicERC1967Upgrade._getSecondaryImplementation();
    }

    /// @inheritdoc Proxy
    function _implementation()
        internal
        view
        override
        returns (address)
    {
        require(msg.data.length >= 4, "NO_FUNC_SIG");
        address _admin = _getAdmin();
        // if there is an owner and it is the sender, delegate to admin logic
        address target = _admin != address(0) && _admin == msg.sender
            ? _secondaryImplementation()
            : _implementation();
        // implementation setters already do an existence check
        // require(target.isContract(), "TARGET_NOT_CONTRACT");
        return target;
    }

    /**
     * @dev unlike transparent upgradeable proxies, this does allow the admin to fallback to a logic contract
     * the admin is expected to interact only with the secondary logic contract, which handles contract
     * upgrades using the UUPS approach
     */
    function _beforeFallback() internal override {
        super._beforeFallback();
    }
}
